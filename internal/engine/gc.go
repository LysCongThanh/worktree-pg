package engine

import (
	"context"
	"sort"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
	"github.com/LysCongThanh/worktree-pg/internal/template"
)

// GC actions.
const (
	gcWouldDrop = template.ActionWouldDrop
	gcDropped   = template.ActionDropped
	gcSkipped   = template.ActionSkipped
)

// GC reconciles this repository's registry entries with `git worktree list`
// (spec §5 "gc"); dry run unless req.Apply (invariant S13):
//   - entries whose path git no longer lists (or lists as prunable) are torn down
//     with Down; locked worktrees are never touched;
//   - stale provisioning leases are rolled back (intent objects dropped);
//   - marked databases/roles of this app that no registry entry knows are dropped
//     when their marker path no longer exists;
//   - templates beyond [template].keep and orphan build databases are dropped.
func (e *Engine) GC(ctx context.Context, req GCRequest) (*GCReport, error) {
	ws, err := e.workspace(ctx, req.Path)
	if err != nil {
		return nil, err
	}
	list, err := ws.repo.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	rep := &GCReport{DryRun: !req.Apply, Items: []GCItem{}, Warnings: []Warning{}}
	var entries []registry.Worktree
	_ = e.Store.View(func(st *registry.State) error {
		for _, w := range st.Worktrees {
			if w.RepoCommonDir == ws.repo.CommonDir {
				entries = append(entries, *w)
			}
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	for i := range entries {
		e.gcEntry(ctx, &entries[i], list, req.Apply, rep)
	}
	if err := e.gcCluster(ctx, ws, req.Apply, false, rep); err != nil {
		rep.Warnings = append(rep.Warnings, Warning{Code: errs.CodeOf(err), Message: "cluster-level gc skipped: " + errs.As(err).Error()})
	}
	return rep, nil
}

// gcEntry classifies one registry entry against the git worktree list.
func (e *Engine) gcEntry(ctx context.Context, w *registry.Worktree, list []gitx.Worktree, apply bool, rep *GCReport) {
	wt, _, listed := gitx.Find(list, w.Path)
	item := GCItem{Kind: "worktree", Name: w.DB, Key: w.Key, Path: w.Path}
	switch {
	case listed && wt.Locked:
		if wt.Prunable || !exists(w.Path) {
			item.Reason, item.Action = "worktree is locked", gcSkipped
			rep.Items = append(rep.Items, item)
		}
		return
	case listed && !wt.Prunable:
		if !exists(w.Path) {
			rep.Warnings = append(rep.Warnings, Warning{Code: "WORKTREE_MISSING", Message: w.Path + " is listed by git but missing; run `git worktree prune`"})
		}
		if w.Status == registry.StatusProvisioning && w.Lease != nil && w.Lease.Stale(e.Opts.Now()) {
			item.Reason = "stale provisioning lease"
			item.Action, item.Reason = gcApply(apply, item.Reason, func() error { return e.rollbackStale(ctx, w) })
			rep.Items = append(rep.Items, item)
		}
		return
	case !listed:
		item.Reason = "not in `git worktree list`"
	default:
		item.Reason = "prunable: " + wt.PrunableReason
	}
	item.Action, item.Reason = gcApply(apply, item.Reason, func() error {
		_, err := e.Down(ctx, DownRequest{Key: w.Key})
		return err
	})
	rep.Items = append(rep.Items, item)
}

// gcApply runs fn when applying and maps the outcome to an action; a failure
// is a skip whose reason carries the error.
func gcApply(apply bool, reason string, fn func() error) (action, why string) {
	if !apply {
		return gcWouldDrop, reason
	}
	if err := fn(); err != nil {
		return gcSkipped, reason + "; " + errs.As(err).Error()
	}
	return gcDropped, reason
}

// rollbackStale drops the objects recorded in a crashed up's intent (S4) and
// marks the entry failed. The worktree never became ready, so nothing is lost.
func (e *Engine) rollbackStale(ctx context.Context, w *registry.Worktree) error {
	if w.Intent != nil {
		cfg := e.configFor(w)
		var c registry.Cluster
		_ = e.Store.View(func(st *registry.State) error {
			if rc := st.Clusters[w.Cluster]; rc != nil {
				c = *rc
			}
			return nil
		})
		cl, admin, err := e.connectRecord(ctx, cfg, &c, true)
		if err != nil {
			return err
		}
		defer admin.Close(context.WithoutCancel(ctx))
		v := &registry.Worktree{Key: w.Key, DB: w.Intent.DB, Role: w.Intent.Role}
		if err := e.backendFor(cl, admin, &workspace{cfg: cfg}).Destroy(ctx, v); err != nil {
			return err
		}
	}
	return e.updateWorktree(ctx, w.Key, func(_ *registry.State, cur *registry.Worktree) {
		if cur.Lease != nil && cur.Lease.Stale(e.Opts.Now()) {
			cur.Lease, cur.Intent, cur.Status = nil, nil, registry.StatusFailed
			cur.LastError = "rolled back by gc after a crashed up"
		}
	})
}
