package engine

import (
	"context"
	"slices"

	"github.com/LysCongThanh/worktree-pg/internal/backend"
	"github.com/LysCongThanh/worktree-pg/internal/cluster"
	"github.com/LysCongThanh/worktree-pg/internal/envfile"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/ports"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
	"github.com/LysCongThanh/worktree-pg/internal/template"
)

// Up provisions the worktree idempotently (spec §5 "Clone"): cluster, registry
// intent + lease + sticky ports, cluster setup, template, role + database clone,
// post_clone_cmd, env files, then the ready record. A re-run with nothing to do
// reports no actions (S11). On failure, objects created by this run are dropped.
func (e *Engine) Up(ctx context.Context, req UpRequest) (*UpResult, error) {
	start := e.Opts.Now()
	ws, err := e.workspace(ctx, req.Path)
	if err != nil {
		return nil, err
	}
	cl, admin, acts, err := e.openCluster(ctx, ws.cfg, !req.DryRun)
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
	res := &UpResult{Worktree: ws.ref(), Database: ws.db, Role: ws.role, Actions: append([]string{}, acts...),
		Cluster:  ClusterRef{ID: cl.ID, Kind: cl.Kind, Host: cl.Host, Port: cl.Port, PGMajor: admin.Info.Major},
		Template: TemplateRef{Name: names.Template(ws.cfg.Name, fp), Fingerprint: fp}, EnvFiles: []envfile.Result{}}
	if req.DryRun {
		return e.dryRun(ws, res)
	}
	bound, _ := ports.DockerBound(ctx) // best effort, outside the registry lock (S10)
	w, crashed, warns, err := e.beginUp(ctx, ws, cl, admin.Info, req, bound)
	if err != nil {
		return nil, err
	}
	res.Ports, res.Warnings = w.Ports, warns
	be := e.backendFor(cl, admin, ws)
	if crashed != nil { // a killed up left this never-ready worktree half-provisioned
		if err := be.Destroy(ctx, &registry.Worktree{Key: ws.key, DB: crashed.DB, Role: crashed.Role}); err != nil {
			e.failUp(ctx, ws, nil, nil, err)
			return nil, err
		}
		res.Actions = append(res.Actions, "rollback_crashed_up")
	}
	var ep backend.Endpoint
	if err := e.provision(ctx, ws, cl, admin, be, w, req, res, &ep); err != nil {
		e.failUp(ctx, ws, func(v *registry.Worktree) error { return be.Destroy(context.WithoutCancel(ctx), v) }, ep.Actions, err)
		return nil, err
	}
	res.DurationMS = e.Opts.Now().Sub(start).Milliseconds()
	return res, nil
}

// provision runs the slow steps after the intent is recorded, then commits.
func (e *Engine) provision(ctx context.Context, ws *workspace, cl *cluster.Cluster, admin *pg.Admin, be backend.Backend,
	w *registry.Worktree, req UpRequest, res *UpResult, ep *backend.Endpoint) error {
	if ran, err := e.runSetup(ctx, ws, cl, admin, req.Wait); err != nil {
		return err
	} else if ran {
		res.Actions = append(res.Actions, "cluster_setup")
	}
	b := e.builder(ws, cl, admin, res.Template.Fingerprint, req.Wait)
	tpl, fp, built, err := e.templateFor(ctx, ws, admin, b, w)
	if err != nil {
		return err
	}
	res.Template = TemplateRef{Name: tpl, Fingerprint: fp, Built: built, Stale: fp != b.FP}
	if built {
		res.Actions = append(res.Actions, "build_template")
	}
	pw, err := e.rolePassword(ws.cfg, &registry.Cluster{ID: cl.ID, Kind: cl.Kind, Container: cl.Container}, ws.role)
	if err != nil {
		return err
	}
	*ep, err = ensureClone(ctx, be, b, e.cloneSpec(ws, cl, tpl, fp, pw))
	res.Actions = append(res.Actions, ep.Actions...)
	if err != nil {
		return err
	}
	cred := pg.Cred{Host: ws.cfg.Database.Host, Port: cl.Port, User: ws.role, Password: pw, Database: ws.db}
	if slices.Contains(ep.Actions, backend.ActCreateDatabase) {
		if err := e.postClone(ctx, ws, cred); err != nil {
			return err
		}
		if ws.cfg.Template.PostCloneCmd != "" {
			res.Actions = append(res.Actions, "post_clone")
		}
	}
	vars, err := renderVars(ws.cfg, varData{App: ws.cfg.Name, Slug: ws.slug, Key: ws.key, Branch: ws.branch, Path: ws.repo.Toplevel},
		w.Ports, cred, e.Opts.Getenv)
	if err != nil {
		return err
	}
	files, recs, warns, err := e.writeEnv(ws, vars, w.EnvFiles)
	if err != nil {
		return err
	}
	res.Warnings = append(res.Warnings, warns...)
	for _, f := range files {
		res.EnvFiles = append(res.EnvFiles, f)
		if f.Changed {
			res.Actions = append(res.Actions, "write_env")
		}
	}
	now := e.Opts.Now()
	return e.updateWorktree(ctx, ws.key, func(st *registry.State, w *registry.Worktree) {
		w.Status, w.Intent, w.LastError = registry.StatusReady, nil, ""
		w.Template, w.TemplateFP, w.EnvFiles = tpl, fp, recs
		w.Release()
		if c := st.Clusters[cl.ID]; c != nil {
			if c.Templates == nil {
				c.Templates = map[string]*registry.Template{}
			}
			t := c.Templates[tpl]
			if t == nil || built {
				t = &registry.Template{App: ws.cfg.Name, FP: fp, BuiltAt: now}
				c.Templates[tpl] = t
			}
			t.LastUsedAt = now
		}
	})
}

// backendFor returns Options.Backend or the database backend on admin.
func (e *Engine) backendFor(cl *cluster.Cluster, admin *pg.Admin, ws *workspace) backend.Backend {
	if e.Opts.Backend != nil {
		return e.Opts.Backend(e)
	}
	return &backend.Database{Admin: admin, ClusterID: cl.ID, Host: ws.cfg.Database.Host, Port: cl.Port}
}

// dryRun reports the planned names and the sticky (or would-be) ports without
// creating anything or writing the registry.
func (e *Engine) dryRun(ws *workspace, res *UpResult) (*UpResult, error) {
	err := e.Store.View(func(st *registry.State) error {
		var sticky map[string]int
		if w := st.Worktrees[ws.key]; w != nil {
			sticky = w.Ports
		}
		a, err := allocator(ws.cfg, st, ws.key, nil, sticky)
		if err != nil {
			return err
		}
		vars := make([]string, 0, len(ws.cfg.Ports.Vars))
		for v := range ws.cfg.Ports.Vars {
			vars = append(vars, v)
		}
		res.Ports, res.Warnings, err = a.Allocate(ws.key, vars, sticky)
		return err
	})
	res.Actions = append(res.Actions, "dry_run")
	return res, err
}
