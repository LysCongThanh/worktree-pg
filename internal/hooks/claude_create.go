package hooks

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/engine"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// createPlan is what ClaudeCreate decided before touching anything.
type createPlan struct {
	repo   *gitx.Repo
	cfg    *config.Config
	hasCfg bool
	path   string // absolute target worktree path
	branch string
	reuse  bool // path is already a worktree on the expected branch
}

// ClaudeCreate handles Claude Code's WorktreeCreate hook: create (or reuse) the
// worktree, copy .worktreeinclude files, provision it with engine.Up and print the
// worktree path as the LAST stdout line. On provisioning failure with
// on_up_failure = "fail" everything created by this run is rolled back.
func ClaudeCreate(ctx context.Context, env Env) error {
	in, err := ReadClaudeInput(env.Stdin)
	if err != nil {
		return err
	}
	if err := ValidName(in.Name); err != nil {
		return err
	}
	p, err := planCreate(ctx, env, in)
	if err != nil {
		return err
	}
	createdPath, createdBranch := false, false
	if !p.reuse {
		createdBranch = !p.repo.BranchExists(ctx, p.branch)
		base := baseRef(ctx, p.repo, p.cfg.Hooks.Claude.Base)
		fmt.Fprintf(env.Stderr, "worktree-pg: git worktree add %s (branch %s from %s)\n", p.path, p.branch, base)
		if err := p.repo.Add(ctx, p.path, p.branch, base, createdBranch, []string{InHookEnv + "=1"}); err != nil {
			return err
		}
		createdPath = true
	}
	if p.cfg.Hooks.Claude.CopyWorktreeInclude && createdPath {
		copied, err := gitx.CopyWorktreeInclude(ctx, p.repo.Toplevel, p.path)
		if err != nil {
			fmt.Fprintf(env.Stderr, "worktree-pg: warning: .worktreeinclude copy incomplete: %v\n", err)
		}
		for _, rel := range copied {
			fmt.Fprintf(env.Stderr, "worktree-pg: copied %s\n", rel)
		}
	}
	if p.hasCfg {
		if err := provision(ctx, env, p, createdBranch); err != nil {
			if p.cfg.Hooks.Claude.OnUpFailure != "fail" {
				fmt.Fprintf(env.Stderr, "worktree-pg: warning: up failed, keeping worktree (on_up_failure=keep): %v\n", err)
			} else {
				rollbackCreate(ctx, env, p, createdPath, createdBranch)
				return err
			}
		}
	} else {
		fmt.Fprintf(env.Stderr, "worktree-pg: no %s in %s; worktree created without a database\n", config.FileName, p.repo.Toplevel)
	}
	real, err := filepath.EvalSymlinks(p.path)
	if err != nil || !isDir(real) {
		return errs.New(errs.Internal, errs.CodeInternal, "worktree %s missing after create", p.path)
	}
	fmt.Fprintln(env.Stdout, filepath.Clean(real))
	return nil
}

// planCreate resolves the project root, config, target path and branch, and
// classifies an already existing target.
func planCreate(ctx context.Context, env Env, in ClaudeInput) (*createPlan, error) {
	root := env.getenv("CLAUDE_PROJECT_DIR")
	if root == "" {
		root = in.Cwd
	}
	if root == "" {
		root = env.Cwd
	}
	repo, err := gitx.Open(ctx, root)
	if err != nil {
		return nil, err
	}
	cfg, has, err := LoadConfig(repo.Toplevel, env.ConfigPath)
	if err != nil {
		return nil, err
	}
	rel, err := render("worktree_dir", cfg.Hooks.Claude.WorktreeDir, in.Name)
	if err != nil {
		return nil, err
	}
	branch, err := render("branch", cfg.Hooks.Claude.Branch, in.Name)
	if err != nil {
		return nil, err
	}
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo.Toplevel, rel)
	}
	p := &createPlan{repo: repo, cfg: cfg, hasCfg: has, path: filepath.Clean(path), branch: branch}
	if !exists(p.path) {
		return p, nil
	}
	list, err := repo.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	wt, idx, ok := gitx.Find(list, p.path)
	if !ok || idx == 0 || wt.Branch != branch {
		return nil, errs.New(errs.Conflict, errs.CodeNotRegistered,
			"%s already exists and is not a worktree on branch %s", p.path, branch).
			WithHint("pick another worktree name or remove the directory yourself")
	}
	p.reuse = true
	return p, nil
}

// provision runs engine.Up for the new worktree and records hook ownership
// (origin, created branch) in the registry so `claude-hook remove` can clean up.
func provision(ctx context.Context, env Env, p *createPlan, createdBranch bool) error {
	wait := p.cfg.Template.Timeout.Duration
	if wait <= 0 {
		wait = 10 * time.Minute
	}
	if _, err := env.Engine.Up(ctx, engine.UpRequest{Path: p.path, Wait: wait, Origin: "claude-hook"}); err != nil {
		return err
	}
	if env.Store == nil {
		return nil
	}
	key := names.Key(resolve(p.path))
	return env.Store.Update(ctx, func(s *registry.State) error {
		if w := s.Worktrees[key]; w != nil {
			w.Origin = "claude-hook"
			if createdBranch {
				w.CreatedBranch = p.branch
			}
		}
		return nil
	})
}

// rollbackCreate undoes this run: Down (database, role, env blocks), then
// `git worktree remove --force` of the path and `git branch -D` of the branch,
// each only if this run created it. A reused worktree keeps its database (it may
// hold data from earlier sessions). Failures are reported, never escalated.
func rollbackCreate(ctx context.Context, env Env, p *createPlan, createdPath, createdBranch bool) {
	ctx = context.WithoutCancel(ctx)
	if !createdPath {
		return
	}
	fmt.Fprintf(env.Stderr, "worktree-pg: provisioning failed, rolling back %s\n", p.path)
	req := engine.DownRequest{Path: p.path, Key: names.Key(resolve(p.path))}
	if _, err := env.Engine.Down(ctx, req); err != nil && !errs.Is(err, errs.CodeNotProvisioned) {
		fmt.Fprintf(env.Stderr, "worktree-pg: rollback: down: %v\n", err)
	}
	if err := p.repo.Remove(ctx, p.path, true); err != nil {
		fmt.Fprintf(env.Stderr, "worktree-pg: rollback: %v\n", err)
	}
	if createdBranch {
		if err := p.repo.DeleteBranch(ctx, p.branch, true); err != nil {
			fmt.Fprintf(env.Stderr, "worktree-pg: rollback: %v\n", err)
		}
	}
}

// baseRef maps [hooks.claude].base to a git revision.
func baseRef(ctx context.Context, repo *gitx.Repo, base string) string {
	switch base {
	case "", "origin-default":
		return repo.DefaultBase(ctx)
	case "head", "HEAD":
		return "HEAD"
	default:
		return base
	}
}
