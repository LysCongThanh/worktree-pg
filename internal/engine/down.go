package engine

import (
	"context"
	"path/filepath"

	"github.com/LysCongThanh/worktree-pg/internal/backend"
	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/envfile"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// Down tears the worktree down idempotently (spec §5 "Teardown"): lease, DROP
// DATABASE WITH (FORCE) and DROP ROLE (markers must match, S1), strip env blocks
// (unless KeepEnv), optionally `git worktree remove`, then delete the registry
// entry. An unregistered worktree is a successful no-op. A role that cannot be
// dropped is reported in Orphans with an exit-1 error and the entry is kept.
func (e *Engine) Down(ctx context.Context, req DownRequest) (*DownResult, error) {
	start := e.Opts.Now()
	key := e.resolveKey(ctx, req)
	w, c, prev, err := e.beginDown(ctx, key)
	res := &DownResult{EnvFiles: []envfile.Result{}, Actions: []string{}, Orphans: []string{}}
	if err != nil || w == nil {
		return res, err
	}
	res.Worktree = WorktreeRef{Path: w.Path, Key: w.Key, Slug: names.Slug(w.Path), Branch: w.Branch}
	res.Database, res.Role = w.DB, w.Role
	cfg := e.configFor(w)
	var main *gitx.Repo
	if req.RemoveWorktree {
		if main, err = checkRemovable(ctx, w.Path); err != nil {
			return res, e.abortDown(ctx, w, prev, err)
		}
	}
	if err := e.destroy(ctx, cfg, w, c, res); err != nil && !errs.Is(err, backend.CodeOrphan) {
		return res, e.abortDown(ctx, w, prev, err)
	} else if err != nil {
		res.Orphans = append(res.Orphans, w.Role)
	}
	if !req.KeepEnv && exists(w.Path) {
		for _, f := range w.EnvFiles {
			changed, err := envfile.Remove(w.Path, f.Path, f.CreatedFile)
			if err != nil {
				res.Warnings = append(res.Warnings, Warning{Code: errs.CodeOf(err), Message: errs.As(err).Error()})
			}
			res.EnvFiles = append(res.EnvFiles, envfile.Result{Path: filepath.Join(w.Path, f.Path), Changed: changed, DuplicateKeys: []string{}})
			if changed {
				res.Actions = append(res.Actions, "strip_env")
			}
		}
	}
	if main != nil {
		if err := main.Remove(ctx, w.Path, cfg.Hooks.Claude.RemoveForce); err != nil {
			res.Warnings = append(res.Warnings, Warning{Code: errs.CodeGitFailed, Message: err.Error()})
		} else {
			res.Actions = append(res.Actions, "remove_worktree")
		}
	}
	err = e.Store.Update(ctx, func(st *registry.State) error {
		if len(res.Orphans) == 0 {
			delete(st.Worktrees, key)
		} else if cur := st.Worktrees[key]; cur != nil {
			cur.Status, cur.LastError, cur.Intent = registry.StatusFailed, "orphaned role "+w.Role, nil
			cur.Release()
		}
		return nil
	})
	res.DurationMS = e.Opts.Now().Sub(start).Milliseconds()
	if err == nil && len(res.Orphans) > 0 {
		err = errs.New(errs.Internal, backend.CodeOrphan, "could not drop %v", res.Orphans).
			WithHint("drop the role's remaining objects, then run `worktree-pg gc --yes`")
	}
	return res, err
}

// destroy connects to the recorded cluster and drops the worktree's objects,
// reporting what existed as actions.
func (e *Engine) destroy(ctx context.Context, cfg *config.Config, w *registry.Worktree, c *registry.Cluster, res *DownResult) error {
	cl, admin, err := e.connectRecord(ctx, cfg, c, true)
	if err != nil {
		return err
	}
	defer admin.Close(context.WithoutCancel(ctx))
	be := e.backendFor(cl, admin, &workspace{cfg: cfg})
	h, err := be.Inspect(ctx, w)
	if err != nil {
		return err
	}
	if err := be.Destroy(ctx, w); err != nil {
		return err
	}
	if h.DBExists {
		res.Actions = append(res.Actions, "drop_database")
	}
	if h.RoleExists && cfg.Database.Roles == "per_worktree" {
		res.Actions = append(res.Actions, "drop_role")
	}
	return nil
}

// resolveKey finds the worktree key: explicit Key, the git toplevel of Path, or
// the registry entry whose recorded path equals Path (directory already gone).
func (e *Engine) resolveKey(ctx context.Context, req DownRequest) string {
	if req.Key != "" {
		return req.Key
	}
	p := e.absPath(req.Path)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	key := ""
	_ = e.Store.View(func(st *registry.State) error { // an exact registered path wins
		for k, w := range st.Worktrees {
			if w.Path == p {
				key = k
			}
		}
		return nil
	})
	if key == "" {
		if repo, err := gitx.Open(ctx, p); err == nil {
			key = names.Key(repo.Toplevel)
		}
	}
	return key
}

// beginDown takes the lease and marks the entry tearing_down; nil when
// unregistered. prev is the status before, restored by abortDown.
func (e *Engine) beginDown(ctx context.Context, key string) (w *registry.Worktree, c *registry.Cluster, prev string, err error) {
	c = &registry.Cluster{}
	err = e.Store.Update(ctx, func(st *registry.State) error {
		cur := st.Worktrees[key]
		if key == "" || cur == nil {
			return nil
		}
		if _, err := cur.Acquire("down", e.Opts.Now()); err != nil {
			return err
		}
		prev, cur.Status = cur.Status, registry.StatusTearingDown
		cp := *cur
		w = &cp
		if rc := st.Clusters[cur.Cluster]; rc != nil {
			*c = *rc
		} else {
			*c = registry.Cluster{ID: cur.Cluster}
		}
		return nil
	})
	return w, c, prev, err
}

// abortDown releases the lease after a refused or failed teardown. A worktree
// that was ready stays ready: its database may hold the developer's data, and a
// non-ready status would let a later killed `up` look like a crashed first
// provisioning whose rollback (next up, gc) drops that database (S4 vs data loss).
func (e *Engine) abortDown(ctx context.Context, w *registry.Worktree, prev string, cause error) error {
	_ = e.updateWorktree(context.WithoutCancel(ctx), w.Key, func(_ *registry.State, cur *registry.Worktree) {
		cur.Status, cur.LastError = registry.StatusFailed, errs.As(cause).Error()
		if prev == registry.StatusReady {
			cur.Status = registry.StatusReady
		}
		cur.Release()
	})
	return cause
}
