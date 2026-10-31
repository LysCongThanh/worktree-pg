package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
	"github.com/LysCongThanh/worktree-pg/internal/hooks"
)

// hookResult is `hook install|uninstall --json` .result.
type hookResult struct {
	Target  string `json:"target"` // claude | git
	Action  string `json:"action"` // install | uninstall
	Path    string `json:"path"`
	Changed bool   `json:"changed"`
	Backup  string `json:"backup,omitempty"`
	Content string `json:"content,omitempty"` // --print, or the snippet when refused
}

// cmdHook handles `hook install|uninstall claude|git`.
func cmdHook(a *app, ctx context.Context, args []string) int {
	if len(args) < 2 || (args[0] != "install" && args[0] != "uninstall") || (args[1] != "claude" && args[1] != "git") {
		return a.finish("hook", nil, nil, usageErr("usage: worktree-pg hook install|uninstall claude|git"), nil)
	}
	r := hookResult{Action: args[0], Target: args[1]}
	name := "hook " + r.Action + " " + r.Target
	fs := a.flagSet(name, "hook install|uninstall claude [--scope local|project|user] [--print] | git [--print]")
	scope := "local"
	if r.Target == "claude" {
		fs.StringVar(&scope, "scope", "local", "settings file: local | project | user")
	}
	printOnly := fs.Bool("print", false, "print the resulting file instead of writing it")
	if code, stop := a.parse(fs, name, args[2:]); stop {
		return code
	}
	if code, stop := a.noArgs(fs, name); stop {
		return code
	}
	install := r.Action == "install"
	var err error
	if r.Target == "claude" {
		err = a.hookClaude(ctx, &r, scope, install, *printOnly)
	} else {
		err = a.hookGit(ctx, &r, install, *printOnly)
	}
	return a.finishKeep(name, r, err, func(w io.Writer) { printHook(w, r, *printOnly) })
}

// hookClaude edits the Claude Code settings file of scope.
func (a *app) hookClaude(ctx context.Context, r *hookResult, scope string, install, printOnly bool) error {
	root := ""
	if scope != "user" {
		repo, err := a.repo(ctx)
		if err != nil {
			return err
		}
		root = repo.Toplevel
	}
	home, _ := os.UserHomeDir()
	path, err := hooks.ClaudeSettingsPath(scope, root, home)
	if err != nil {
		return err
	}
	r.Path = path
	out, changed, err := hooks.EditClaudeSettings(path, install)
	if err != nil {
		return err
	}
	return a.applyHookFile(r, out, changed, false, printOnly, 0o644)
}

// hookGit edits the post-checkout hook. A hooks path managed inside the work
// tree (husky) is refused; the snippet is returned for the user to add.
func (a *app) hookGit(ctx context.Context, r *hookResult, install, printOnly bool) error {
	path, err := hooks.GitHookPath(ctx, a.pathOrDot())
	r.Path = path
	if errs.Is(err, hooks.CodeHooksPathManaged) {
		r.Content = hooks.GitHookBlock()
		return err
	}
	if err != nil {
		return err
	}
	old, rerr := os.ReadFile(path)
	if rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return errs.Wrap(errs.Internal, errs.CodeInternal, rerr, "read %s", path)
	}
	out, changed, remove := hooks.EditGitHook(old, install)
	if install && hooks.ExitsEarly(old) {
		fmt.Fprintf(a.stderr, "worktree-pg: warning: %s has a top-level `exit`; move the worktree-pg block above it\n", path)
	}
	return a.applyHookFile(r, out, changed, remove, printOnly, 0o755)
}

// applyHookFile prints (--print) or writes the new content with a .bak backup.
func (a *app) applyHookFile(r *hookResult, out []byte, changed, remove, printOnly bool, mode fs.FileMode) error {
	r.Changed = changed
	if printOnly {
		r.Content = string(out)
		return nil
	}
	if !changed {
		return nil
	}
	if _, err := os.Stat(r.Path); err == nil {
		r.Backup = r.Path + ".bak"
	}
	if remove {
		if err := os.Rename(r.Path, r.Backup); err != nil {
			return errs.Wrap(errs.Internal, errs.CodeInternal, err, "remove %s", r.Path)
		}
		return nil
	}
	return hooks.WriteWithBackup(r.Path, out, mode)
}

// printHook renders the human outcome.
func printHook(w io.Writer, r hookResult, printOnly bool) {
	if r.Content != "" {
		if !printOnly {
			fmt.Fprintf(w, "add this to your post-checkout hook (%s):\n\n", r.Path)
		}
		fmt.Fprint(w, r.Content)
		return
	}
	state := "already up to date"
	if r.Changed {
		state = r.Action + "ed"
	}
	fmt.Fprintf(w, "%s hook %s: %s\n", r.Target, state, r.Path)
	if r.Backup != "" {
		fmt.Fprintf(w, "backup: %s\n", r.Backup)
	}
}

// repo opens the git repository at --path (or the working directory).
func (a *app) repo(ctx context.Context) (*gitx.Repo, error) {
	return gitx.Open(ctx, a.pathOrDot())
}

// pathOrDot returns --path or ".".
func (a *app) pathOrDot() string {
	if p, err := a.path(); err == nil {
		return p
	}
	return "."
}
