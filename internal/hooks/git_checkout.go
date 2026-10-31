package hooks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/engine"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
)

// GitPostCheckout is the body of the git post-checkout hook
// (`worktree-pg git-hook post-checkout <old> <new> <flag>`). It provisions only
// for a brand-new checkout (old ref is the null id, i.e. `git worktree add` or
// `git clone`, flag = 1 for a branch checkout), when [hooks.git].auto_up is true,
// the repository has a config file and worktree-pg is not itself running
// `git worktree add`. It ALWAYS returns 0: a git hook must never break checkout.
// open is called lazily so ordinary checkouts never touch the state directory.
func GitPostCheckout(ctx context.Context, env Env, args []string, open func() (Provisioner, error)) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(env.Stderr, "worktree-pg: post-checkout: internal error: %v\n", r)
			code = 0
		}
	}()
	path, why := postCheckoutTarget(ctx, env, args)
	if path == "" {
		if why != "" && env.getenv("WORKTREE_PG_DEBUG") != "" {
			fmt.Fprintf(env.Stderr, "worktree-pg: post-checkout skipped: %s\n", why)
		}
		return 0
	}
	eng, err := open()
	if err == nil {
		fmt.Fprintf(env.Stderr, "worktree-pg: provisioning new worktree %s\n", path)
		_, err = eng.Up(ctx, engine.UpRequest{Path: path, Wait: 10 * time.Minute, Origin: "git-hook"})
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "worktree-pg: post-checkout: up failed (checkout kept): %v\n", err)
		fmt.Fprintln(env.Stderr, "worktree-pg: fix the problem, then run `worktree-pg up` in the worktree")
	}
	return 0
}

// postCheckoutTarget returns the worktree to provision, or "" and the reason it
// is skipped.
func postCheckoutTarget(ctx context.Context, env Env, args []string) (path, why string) {
	if len(args) != 3 {
		return "", "expected <old> <new> <flag>"
	}
	if !isNullRef(args[0]) {
		return "", "not a new checkout"
	}
	if args[2] != "1" {
		return "", "file checkout"
	}
	if env.getenv(InHookEnv) != "" {
		return "", "worktree created by worktree-pg itself"
	}
	repo, err := gitx.Open(ctx, env.Cwd)
	if err != nil {
		return "", err.Error()
	}
	cfg, found, err := LoadConfig(repo.Toplevel, env.ConfigPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "worktree-pg: post-checkout: %v\n", err)
		return "", "invalid config"
	}
	if !found {
		return "", "no config file"
	}
	if !cfg.Hooks.Git.AutoUp {
		return "", "hooks.git.auto_up = false"
	}
	return repo.Toplevel, ""
}

// isNullRef reports whether ref is git's all-zero object id (SHA-1 or SHA-256).
func isNullRef(ref string) bool {
	return (len(ref) == 40 || len(ref) == 64) && strings.Trim(ref, "0") == ""
}
