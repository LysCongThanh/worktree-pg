package engine

import (
	"context"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/cluster"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/ports"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// leasePoll is how often `up --wait` re-tries a held lease.
const leasePoll = 250 * time.Millisecond

// beginUp records the intent of an up under the registry lock (fast, S10): the
// cluster record, the worktree entry with its lease, names, intent and sticky
// ports. A held lease is retried until req.Wait elapses (else LEASE_HELD, exit 6).
// crashed is the intent of an earlier up of a never-ready worktree that died
// holding the lease (kill -9): its objects may be half-provisioned (post_clone_cmd
// cut short), so the caller rolls them back before provisioning again (S4).
func (e *Engine) beginUp(ctx context.Context, ws *workspace, cl *cluster.Cluster, info pg.ServerInfo, req UpRequest, bound ports.PortSet) (w *registry.Worktree, crashed *registry.Intent, warns []Warning, err error) {
	deadline := time.Now().Add(req.Wait)
	for {
		w, crashed, warns, err = e.tryBeginUp(ctx, ws, cl, info, req, bound)
		if !errs.Is(err, errs.CodeLeaseHeld) || time.Now().After(deadline) {
			return w, crashed, warns, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, nil, ctx.Err()
		case <-time.After(leasePoll):
		}
	}
}

func (e *Engine) tryBeginUp(ctx context.Context, ws *workspace, cl *cluster.Cluster, info pg.ServerInfo, req UpRequest, bound ports.PortSet) (*registry.Worktree, *registry.Intent, []Warning, error) {
	var out registry.Worktree
	var crashed *registry.Intent
	var warns []Warning
	now := e.Opts.Now()
	err := e.Store.Update(ctx, func(st *registry.State) error {
		c := st.Clusters[cl.ID]
		if c == nil {
			c = &registry.Cluster{ID: cl.ID}
			st.Clusters[cl.ID] = c
		}
		c.Kind, c.Image, c.Container, c.Host, c.Port, c.PGMajor = cl.Kind, cl.Image, cl.Container, cl.Host, cl.Port, info.Major
		w := st.Worktrees[ws.key]
		if w == nil {
			w = &registry.Worktree{Key: ws.key, CreatedAt: now, Origin: req.Origin, Status: registry.StatusProvisioning, Ports: map[string]int{}}
			st.Worktrees[ws.key] = w
		}
		stale, err := w.Acquire("up", now)
		if err != nil {
			return err
		}
		if stale && w.Status != registry.StatusReady && w.Intent != nil {
			cp := *w.Intent
			crashed = &cp
		}
		w.Path, w.RepoCommonDir, w.App, w.Branch = ws.repo.Toplevel, ws.repo.CommonDir, ws.cfg.Name, ws.branch
		w.Cluster, w.DB, w.Role = cl.ID, ws.db, ws.role
		w.Intent = &registry.Intent{DB: ws.db, Role: ws.role}
		if !ws.perWorktree() {
			w.Intent.Role = ""
		}
		if w.Status != registry.StatusReady {
			w.Status = registry.StatusProvisioning
		}
		sticky := w.Ports
		if req.ReassignPorts {
			sticky = nil
		}
		a, err := allocator(ws.cfg, st, ws.key, bound, w.Ports)
		if err != nil {
			return errs.Wrap(errs.Config, errs.CodeConfigInvalid, err, "ports")
		}
		vars := make([]string, 0, len(ws.cfg.Ports.Vars))
		for v := range ws.cfg.Ports.Vars {
			vars = append(vars, v)
		}
		p, ws2, err := a.Allocate(ws.key, vars, sticky)
		if err != nil {
			return err
		}
		w.Ports, warns, w.UpdatedAt = p, ws2, now
		out = *w
		return nil
	})
	return &out, crashed, warns, err
}

// updateWorktree applies fn to the registered entry key (no-op when absent).
func (e *Engine) updateWorktree(ctx context.Context, key string, fn func(st *registry.State, w *registry.Worktree)) error {
	return e.Store.Update(ctx, func(st *registry.State) error {
		if w := st.Worktrees[key]; w != nil {
			fn(st, w)
			w.UpdatedAt = e.Opts.Now()
		}
		return nil
	})
}

// failUp is the rollback of a failed up (invariant S4): objects created by this
// run are dropped, the entry is marked failed (ports stay sticky) and the lease
// released. A previously ready worktree keeps its status.
func (e *Engine) failUp(ctx context.Context, ws *workspace, drop func(*registry.Worktree) error, created []string, cause error) {
	ctx = context.WithoutCancel(ctx)
	victim := &registry.Worktree{Key: ws.key}
	for _, a := range created {
		switch a {
		case "create_database":
			victim.DB = ws.db
		case "create_role":
			victim.Role = ws.role
		}
	}
	if drop != nil && (victim.DB != "" || victim.Role != "") {
		_ = drop(victim)
	}
	_ = e.updateWorktree(ctx, ws.key, func(_ *registry.State, w *registry.Worktree) {
		if w.Status != registry.StatusReady {
			w.Status = registry.StatusFailed
		}
		w.LastError, w.Intent = errs.As(cause).Error(), nil
		w.Release()
	})
}
