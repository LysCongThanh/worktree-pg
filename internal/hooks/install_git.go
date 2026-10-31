package hooks

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
)

// CodeHooksPathManaged: core.hooksPath points inside the work tree (husky,
// lefthook, ...). Those files are committed and owned by another tool, so
// worktree-pg refuses to edit them and prints a snippet instead (exit 2).
const CodeHooksPathManaged = "HOOKS_PATH_MANAGED"

// Marked block appended to (or forming) the post-checkout hook.
const (
	GitBlockBegin = "# >>> worktree-pg post-checkout >>>"
	GitBlockEnd   = "# <<< worktree-pg post-checkout <<<"
	shebang       = "#!/bin/sh"
)

// GitHookBlock is the shell snippet that calls worktree-pg from post-checkout.
// It never fails the checkout, even when worktree-pg is not installed.
func GitHookBlock() string {
	return GitBlockBegin + "\n" +
		"# Provisions a database for new worktrees (git worktree add); never fails the checkout.\n" +
		`if command -v worktree-pg >/dev/null 2>&1; then worktree-pg git-hook post-checkout "$@" || true; fi` + "\n" +
		GitBlockEnd + "\n"
}

// GitHookPath returns the absolute post-checkout path git will run for the repo
// at dir (honours core.hooksPath and linked worktrees). A hooks path inside the
// work tree is refused with CodeHooksPathManaged.
func GitHookPath(ctx context.Context, dir string) (string, error) {
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return "", err
	}
	p, err := gitx.Git(ctx, repo.Toplevel, nil, "rev-parse", "--path-format=absolute", "--git-path", "hooks/post-checkout")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(repo.Toplevel, p)
	}
	hp, _ := gitx.Git(ctx, repo.Toplevel, nil, "config", "--get", "core.hooksPath")
	if hp != "" && inside(resolve(filepath.Dir(p)), repo.Toplevel) {
		return p, errs.New(errs.Conflict, CodeHooksPathManaged,
			"core.hooksPath=%s is inside the work tree (husky or similar); not editing committed hooks", hp).
			WithHint("add the printed snippet to your post-checkout hook manager yourself")
	}
	return p, nil
}

// EditGitHook installs or removes the marked block in an existing hook script
// (old may be nil). Foreign hook content is preserved: the block is appended or
// replaced in place. remove reports that only a bare shebang would be left, in
// which case the caller deletes the file.
func EditGitHook(old []byte, install bool) (out []byte, changed, remove bool) {
	text := string(old)
	stripped := stripBlock(text)
	if install {
		base := stripped
		if strings.TrimSpace(base) == "" {
			base = shebang + "\n"
		}
		if !strings.HasSuffix(base, "\n") {
			base += "\n"
		}
		out := base + GitHookBlock()
		if strings.Contains(text, GitBlockBegin) && len(stripped) < len(text) {
			out = replaceBlock(text) // keep the block where it already is
		}
		return []byte(out), out != text, false
	}
	if stripped == text {
		return old, false, false
	}
	rest := strings.TrimSpace(stripped)
	return []byte(stripped), true, rest == "" || rest == shebang
}

// ExitsEarly reports whether a foreign hook has a top-level `exit` that would stop
// an appended block from running.
func ExitsEarly(hook []byte) bool {
	return exitRe.Match([]byte(stripBlock(string(hook))))
}

var exitRe = regexp.MustCompile(`(?m)^exit\b`)

// stripBlock removes the marked block (begin..end lines inclusive).
func stripBlock(text string) string {
	b := strings.Index(text, GitBlockBegin)
	if b < 0 {
		return text
	}
	e := strings.Index(text[b:], GitBlockEnd)
	if e < 0 {
		return text // unterminated block: leave the file alone
	}
	end := b + e + len(GitBlockEnd)
	if end < len(text) && text[end] == '\n' {
		end++
	}
	return text[:b] + text[end:]
}

// replaceBlock rewrites an existing block with the current one, in place.
func replaceBlock(text string) string {
	b := strings.Index(text, GitBlockBegin)
	e := strings.Index(text[b:], GitBlockEnd)
	end := b + e + len(GitBlockEnd)
	if end < len(text) && text[end] == '\n' {
		end++
	}
	return text[:b] + GitHookBlock() + text[end:]
}

// inside reports whether p is root or below it.
func inside(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}
