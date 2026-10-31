package cli

import (
	"context"
	"os"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/hooks"
)

// cmdClaudeHook runs `claude-hook create|remove` (called by Claude Code). stdout
// is reserved for the worktree path printed by create, so errors always go to
// stderr as text and --json is ignored.
func cmdClaudeHook(a *app, ctx context.Context, args []string) int {
	a.g.JSON = false
	if len(args) != 1 || (args[0] != "create" && args[0] != "remove") {
		return a.finish("claude-hook", nil, nil, usageErr("usage: worktree-pg claude-hook create|remove (JSON on stdin)"), nil)
	}
	e, cwd, err := a.engine()
	if err != nil {
		return a.finish("claude-hook", nil, nil, err, nil)
	}
	env := hooks.Env{
		Stdin: a.stdin, Stdout: a.stdout, Stderr: a.stderr, Getenv: a.getenv,
		Cwd: cwd, ConfigPath: a.g.Config, Engine: e, Store: e.Store,
	}
	if args[0] == "create" {
		err = hooks.ClaudeCreate(ctx, env)
	} else {
		err = hooks.ClaudeRemove(ctx, env)
	}
	if err != nil {
		printError(a.stderr, err)
	}
	return errs.ExitCode(err)
}

// cmdGitHook runs `git-hook post-checkout <old> <new> <flag>`. It always exits 0.
func cmdGitHook(a *app, ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "post-checkout" {
		printError(a.stderr, usageErr("usage: worktree-pg git-hook post-checkout <old> <new> <flag>"))
		return exitOK
	}
	// git exports GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE to hooks; they would pin
	// every nested git command (and user commands) to this one worktree.
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_PREFIX"} {
		_ = os.Unsetenv(k)
	}
	cwd, err := a.path()
	if err != nil {
		return exitOK
	}
	env := hooks.Env{Stdout: a.stderr, Stderr: a.stderr, Getenv: a.getenv, Cwd: cwd, ConfigPath: a.g.Config}
	open := func() (hooks.Provisioner, error) {
		e, _, err := a.engine()
		return e, err
	}
	return hooks.GitPostCheckout(ctx, env, args[1:], open)
}
