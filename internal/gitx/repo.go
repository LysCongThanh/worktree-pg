// Package gitx is a thin wrapper over the git CLI (os/exec): repository discovery,
// `git worktree list --porcelain -z` parsing, worktree add/remove and the
// .worktreeinclude copy that Claude Code performs natively.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// Repo describes the worktree a command runs in.
type Repo struct {
	Toplevel  string // symlink-resolved absolute worktree root
	CommonDir string // shared .git dir (same for every worktree of the repo)
	GitDir    string // this worktree's git dir
}

// Git runs `git -C dir args...` with extra env and returns trimmed stdout. A
// non-zero exit becomes GIT_FAILED carrying git's stderr.
func Git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := gitRaw(ctx, dir, env, args...)
	return strings.TrimRight(string(out), "\n"), err
}

// gitRaw is Git without trimming (needed for -z output).
func gitRaw(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := commandIn(ctx, dir, args...)
	cmd.Env = append(cmd.Env, env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errs.Wrap(errs.Prereq, errs.CodeGitUnavailable, err, "git not found on PATH")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	msg := strings.TrimSpace(stderr.String())
	return nil, errs.Wrap(errs.Internal, errs.CodeGitFailed, err, "git %s: %s", strings.Join(args, " "), msg)
}

// commandIn prepares `git -C dir args...` inheriting the environment.
func commandIn(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = os.Environ()
	return cmd
}

// Open discovers the repository containing path.
func Open(ctx context.Context, path string) (*Repo, error) {
	out, err := Git(ctx, path, nil, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--git-dir")
	if err != nil {
		if errs.CodeOf(err) == errs.CodeGitFailed {
			return nil, errs.Wrap(errs.Config, errs.CodeNotGitRepo, err, "%s is not inside a git work tree", path)
		}
		return nil, err
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		return nil, errs.New(errs.Config, errs.CodeNotGitRepo, "%s is not inside a git work tree (bare repository?)", path)
	}
	top, err := filepath.EvalSymlinks(lines[0])
	if err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "resolve toplevel")
	}
	return &Repo{Toplevel: top, CommonDir: filepath.Clean(lines[1]), GitDir: filepath.Clean(lines[2])}, nil
}

// IsMain reports whether this is the main worktree (not a linked one).
func (r *Repo) IsMain() bool { return r.CommonDir == r.GitDir }

// CurrentBranch returns the short branch name, or "" when HEAD is detached.
func (r *Repo) CurrentBranch(ctx context.Context) string {
	out, err := Git(ctx, r.Toplevel, nil, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// DefaultBase returns origin's default branch ("origin/main") when known, else "HEAD".
func (r *Repo) DefaultBase(ctx context.Context) string {
	out, err := Git(ctx, r.Toplevel, nil, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err != nil || out == "" || out == "origin/HEAD" {
		return "HEAD"
	}
	return out
}

// BranchExists reports whether refs/heads/<name> exists.
func (r *Repo) BranchExists(ctx context.Context, name string) bool {
	_, err := Git(ctx, r.Toplevel, nil, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}
