package engine

import (
	"context"
	"sort"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/backend"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
	"github.com/LysCongThanh/worktree-pg/internal/template"
)

// statusProbeTimeout bounds the live health probe of `status` (never starts containers).
const statusProbeTimeout = 5 * time.Second

// Status reports the current worktree (or every registered one with All) from the
// registry. For the current worktree it also probes live health and whether the
// template inputs changed since the clone was made (template_stale). An
// unregistered current worktree yields an empty list.
func (e *Engine) Status(ctx context.Context, req StatusRequest) ([]WorktreeStatus, error) {
	var ws *workspace
	if !req.All {
		var err error
		if ws, err = e.workspace(ctx, req.Path); err != nil {
			return nil, err
		}
	}
	var list []WorktreeStatus
	var recs []registry.Worktree
	clusters := map[string]registry.Cluster{}
	err := e.Store.View(func(st *registry.State) error {
		for k, w := range st.Worktrees {
			if ws == nil || k == ws.key {
				recs = append(recs, *w)
			}
		}
		for id, c := range st.Clusters {
			clusters[id] = *c
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Path < recs[j].Path })
	for i := range recs {
		w, c := &recs[i], clusters[recs[i].Cluster]
		s := WorktreeStatus{
			Worktree: WorktreeRef{Path: w.Path, Key: w.Key, Slug: names.Slug(w.Path), Branch: w.Branch},
			Status:   w.Status, Database: w.DB, Role: w.Role, Ports: w.Ports, EnvFiles: []string{},
			Cluster:  ClusterRef{ID: c.ID, Kind: c.Kind, Host: c.Host, Port: c.Port, PGMajor: c.PGMajor},
			Template: TemplateRef{Name: w.Template, Fingerprint: w.TemplateFP}, PathExists: exists(w.Path), LastError: w.LastError,
		}
		for _, f := range w.EnvFiles {
			s.EnvFiles = append(s.EnvFiles, f.Path)
		}
		if ws != nil {
			s.Health = e.probe(ctx, ws, w, &c)
			s.Template.Stale = s.Health.TemplateStale
		}
		list = append(list, s)
	}
	if list == nil {
		list = []WorktreeStatus{}
	}
	return list, nil
}

// probe inspects the live objects of the current worktree (best effort: a
// connection failure is reported in Detail, never as an error).
func (e *Engine) probe(ctx context.Context, ws *workspace, w *registry.Worktree, c *registry.Cluster) *backend.Health {
	h := &backend.Health{}
	fp, err := template.Fingerprint(ws.cfg.Root, ws.cfg, pg.ServerInfo{Major: c.PGMajor}, c.Image)
	if err == nil && w.TemplateFP != "" {
		h.TemplateStale = fp != w.TemplateFP
	}
	ctx, cancel := context.WithTimeout(ctx, statusProbeTimeout)
	defer cancel()
	cl, admin, err := e.connectRecord(ctx, ws.cfg, c, false)
	if err != nil {
		h.Detail = err.Error()
		return h
	}
	defer admin.Close(context.WithoutCancel(ctx))
	got, err := e.backendFor(cl, admin, ws).Inspect(ctx, w)
	if err != nil {
		h.Detail = err.Error()
		return h
	}
	got.TemplateStale = h.TemplateStale
	return &got
}
