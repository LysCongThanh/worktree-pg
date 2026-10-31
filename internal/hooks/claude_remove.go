package hooks

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/engine"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// ClaudeRemove handles Claude Code's WorktreeRemove hook.
//
//   - directory already gone: tear down the registry entry with that path (by key), exit 0
//   - otherwise the path must be a listed, non-main, unlocked worktree (else exit 2,
//     directory intact); then Down, `git worktree remove [--force]` and
//     `git branch -d <created_branch>` (merged only).
func ClaudeRemove(ctx context.Context, env Env) error {
	in, err := ReadClaudeInput(env.Stdin)
	if err != nil {
		return err
	}
	if in.WorktreePath == "" || !filepath.IsAbs(in.WorktreePath) {
		return badInput("worktree_path must be an absolute path, got %q", in.WorktreePath)
	}
	path := filepath.Clean(in.WorktreePath)
	entry, err := FindEntry(env.Store, path)
	if err != nil {
		return err
	}
	if !exists(path) {
		if entry == nil {
			fmt.Fprintf(env.Stderr, "worktree-pg: %s is gone and not registered; nothing to do\n", path)
			return nil
		}
		_, err := env.Engine.Down(ctx, engine.DownRequest{Path: entry.Path, Key: entry.Key})
		return ignoreNotProvisioned(err)
	}
	rm, err := CheckRemovable(ctx, path)
	if err != nil {
		return err
	}
	cfg, _, err := LoadConfig(rm.Worktree.Path, env.ConfigPath)
	if err != nil {
		// a broken config must not strand the worktree: fall back to defaults
		fmt.Fprintf(env.Stderr, "worktree-pg: warning: %v; using default hook settings\n", err)
		cfg = config.Default()
	}
	branch := ""
	if entry != nil {
		branch = entry.CreatedBranch
		if _, err := env.Engine.Down(ctx, engine.DownRequest{Path: entry.Path, Key: entry.Key}); ignoreNotProvisioned(err) != nil {
			return err
		}
	}
	return rm.Remove(ctx, cfg.Hooks.Claude.RemoveForce, branch, env.Stderr)
}

// ignoreNotProvisioned treats "nothing to tear down" as success (idempotent remove).
func ignoreNotProvisioned(err error) error {
	if errs.Is(err, errs.CodeNotProvisioned) {
		return nil
	}
	return err
}
