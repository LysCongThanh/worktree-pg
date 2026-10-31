package engine

import (
	"context"
	"strings"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
	"github.com/LysCongThanh/worktree-pg/internal/template"
)

// gcCluster handles the database-level part of gc on this repository's cluster:
// unrecorded marked clones/roles of the app (unless templatesOnly), extra
// templates and orphan builds.
func (e *Engine) gcCluster(ctx context.Context, ws *workspace, apply, templatesOnly bool, rep *GCReport) error {
	id, err := e.clusterID(ws.cfg)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	var rec registry.Cluster
	_ = e.Store.View(func(st *registry.State) error {
		for k := range st.Worktrees {
			known[k] = true
		}
		if c := st.Clusters[id]; c != nil {
			rec = *c
		}
		return nil
	})
	if rec.ID == "" {
		return nil // nothing was ever provisioned on this cluster
	}
	_, admin, err := e.connectRecord(ctx, ws.cfg, &rec, apply)
	if err != nil {
		return err
	}
	defer admin.Close(context.WithoutCancel(ctx))
	if !templatesOnly {
		if err := e.gcUnrecorded(ctx, admin, ws.cfg.Name+"_wt_", known, apply, rep); err != nil {
			return err
		}
	}
	return e.gcTemplates(ctx, ws, admin, &rec, apply, rep)
}

// gcUnrecorded handles marked clones and roles whose key no registry entry knows.
func (e *Engine) gcUnrecorded(ctx context.Context, admin *pg.Admin, prefix string, known map[string]bool, apply bool, rep *GCReport) error {
	dbs, err := admin.MarkedDatabases(ctx)
	if err != nil {
		return err
	}
	for _, d := range dbs {
		m, ok := names.ParseMarker(d.Comment)
		if !ok || m.Kind != names.KindClone || known[m.Key] || !strings.HasPrefix(d.Name, prefix) {
			continue
		}
		e.gcOrphan(rep, GCItem{Kind: "database", Name: d.Name, Key: m.Key, Path: m.Path}, apply,
			func() error { return admin.DropDatabase(ctx, d.Name, true) })
	}
	roles, err := admin.MarkedRoles(ctx)
	if err != nil {
		return err
	}
	for _, r := range roles {
		m, ok := names.ParseMarker(r.Comment)
		if !ok || m.Kind != names.KindRole || known[m.Key] || !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		e.gcOrphan(rep, GCItem{Kind: "role", Name: r.Name, Key: m.Key, Path: m.Path}, apply,
			func() error { return admin.DropRole(ctx, r.Name) })
	}
	return nil
}

// gcTemplates keeps [template].keep templates (always the current one).
func (e *Engine) gcTemplates(ctx context.Context, ws *workspace, admin *pg.Admin, rec *registry.Cluster, apply bool, rep *GCReport) error {
	fp, err := template.Fingerprint(ws.cfg.Root, ws.cfg, admin.Info, rec.Image)
	if err != nil {
		return err
	}
	recency := map[string]time.Time{}
	for n, t := range rec.Templates {
		recency[n] = t.LastUsedAt
	}
	cands, err := template.GC(ctx, admin, template.GCOptions{App: ws.cfg.Name, Keep: ws.cfg.Template.Keep,
		Current: names.Template(ws.cfg.Name, fp), Recency: recency, Apply: apply})
	for _, c := range cands {
		rep.Items = append(rep.Items, GCItem{Kind: c.Kind, Name: c.Name, Reason: c.Reason, Action: c.Action})
	}
	if apply && err == nil {
		e.forgetTemplates(ctx, rec.ID, cands)
	}
	return err
}

// gcOrphan reports (and with apply drops) an unrecorded marked object, but only
// when its marker path is no longer a live directory.
func (e *Engine) gcOrphan(rep *GCReport, item GCItem, apply bool, drop func() error) {
	if item.Path != "" && exists(item.Path) {
		item.Reason, item.Action = "unrecorded but its worktree path still exists", gcSkipped
	} else {
		item.Action, item.Reason = gcApply(apply, "unrecorded object of a removed worktree", drop)
	}
	rep.Items = append(rep.Items, item)
}

// forgetTemplates removes dropped templates from the cluster record.
func (e *Engine) forgetTemplates(ctx context.Context, id string, cands []template.Candidate) {
	_ = e.Store.Update(ctx, func(st *registry.State) error {
		if c := st.Clusters[id]; c != nil {
			for _, cd := range cands {
				if cd.Action == gcDropped {
					delete(c.Templates, cd.Name)
				}
			}
		}
		return nil
	})
}
