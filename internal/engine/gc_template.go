package engine

import (
	"context"
	"sort"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/cluster"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
	"github.com/LysCongThanh/worktree-pg/internal/template"
)

// TemplateInfo is one element of `template status` .result.
type TemplateInfo struct {
	Name        string    `json:"name"`
	Fingerprint string    `json:"fingerprint"`
	Current     bool      `json:"current"` // matches the current inputs
	BuiltAt     time.Time `json:"built_at,omitzero"`
	LastUsedAt  time.Time `json:"last_used_at,omitzero"`
	Clones      int       `json:"clones"` // registered worktrees cloned from it
}

// TemplateRefreshResult is `template refresh` .result.
type TemplateRefreshResult struct {
	Template TemplateRef `json:"template"`
	Actions  []string    `json:"actions"`
}

// TemplateStatus lists the app's published templates on its cluster.
func (e *Engine) TemplateStatus(ctx context.Context, path string) (any, error) {
	ws, err := e.workspace(ctx, path)
	if err != nil {
		return nil, err
	}
	cl, admin, _, err := e.openCluster(ctx, ws.cfg, false)
	if err != nil {
		return nil, err
	}
	defer admin.Close(context.WithoutCancel(ctx))
	fp, err := template.Fingerprint(ws.cfg.Root, ws.cfg, admin.Info, cl.Image)
	if err != nil {
		return nil, err
	}
	dbs, err := admin.MarkedDatabases(ctx)
	if err != nil {
		return nil, err
	}
	var rec registry.Cluster
	clones := map[string]int{}
	_ = e.Store.View(func(st *registry.State) error {
		if c := st.Clusters[cl.ID]; c != nil {
			rec = *c
		}
		for _, w := range st.Worktrees {
			clones[w.Template]++
		}
		return nil
	})
	out := []TemplateInfo{}
	for _, d := range dbs {
		m, ok := names.ParseMarker(d.Comment)
		if !ok || m.Kind != names.KindTemplate || d.Name != names.Template(ws.cfg.Name, m.FP) {
			continue
		}
		ti := TemplateInfo{Name: d.Name, Fingerprint: m.FP, Current: m.FP == fp, Clones: clones[d.Name]}
		if t := rec.Templates[d.Name]; t != nil {
			ti.BuiltAt, ti.LastUsedAt = t.BuiltAt, t.LastUsedAt
		}
		out = append(out, ti)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsedAt.After(out[j].LastUsedAt) })
	return out, nil
}

// TemplateRefresh builds the template for the current inputs (force: rebuild it
// even when it exists). Existing clones are untouched.
func (e *Engine) TemplateRefresh(ctx context.Context, path string, force bool) (any, error) {
	ws, err := e.workspace(ctx, path)
	if err != nil {
		return nil, err
	}
	cl, admin, acts, err := e.openCluster(ctx, ws.cfg, true)
	if err != nil {
		return nil, err
	}
	defer admin.Close(context.WithoutCancel(ctx))
	if err := cluster.CheckCaps(admin.Info, ws.cfg.Database.Roles); err != nil {
		return nil, err
	}
	ws.useAdminRole(admin.Info.User)
	fp, err := template.Fingerprint(ws.cfg.Root, ws.cfg, admin.Info, cl.Image)
	if err != nil {
		return nil, err
	}
	res := &TemplateRefreshResult{Actions: append([]string{}, acts...)}
	if err := e.recordCluster(ctx, cl, admin.Info.Major); err != nil {
		return nil, err
	}
	if ran, err := e.runSetup(ctx, ws, cl, admin, 0); err != nil {
		return nil, err
	} else if ran {
		res.Actions = append(res.Actions, "cluster_setup")
	}
	b := e.builder(ws, cl, admin, fp, 0)
	b.Force = force
	name, built, err := b.Ensure(ctx)
	if err != nil {
		return nil, err
	}
	if built {
		res.Actions = append(res.Actions, "build_template")
	}
	res.Template = TemplateRef{Name: name, Fingerprint: fp, Built: built}
	now := e.Opts.Now()
	return res, e.Store.Update(ctx, func(st *registry.State) error {
		if c := st.Clusters[cl.ID]; c != nil && built {
			if c.Templates == nil {
				c.Templates = map[string]*registry.Template{}
			}
			c.Templates[name] = &registry.Template{App: ws.cfg.Name, FP: fp, BuiltAt: now, LastUsedAt: now}
		}
		return nil
	})
}

// recordCluster makes sure the cluster has a registry record.
func (e *Engine) recordCluster(ctx context.Context, cl *cluster.Cluster, major int) error {
	return e.Store.Update(ctx, func(st *registry.State) error {
		c := st.Clusters[cl.ID]
		if c == nil {
			c = &registry.Cluster{ID: cl.ID}
			st.Clusters[cl.ID] = c
		}
		c.Kind, c.Image, c.Container, c.Host, c.Port, c.PGMajor = cl.Kind, cl.Image, cl.Container, cl.Host, cl.Port, major
		return nil
	})
}

// TemplateGC keeps [template].keep templates (always the current one) and drops
// the rest plus orphan build databases; dry run unless apply (invariant S13).
func (e *Engine) TemplateGC(ctx context.Context, path string, apply bool) (any, error) {
	ws, err := e.workspace(ctx, path)
	if err != nil {
		return nil, err
	}
	rep := &GCReport{DryRun: !apply, Items: []GCItem{}, Warnings: []Warning{}}
	return rep, e.gcCluster(ctx, ws, apply, true, rep)
}
