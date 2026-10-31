package engine

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/backend"
	"github.com/LysCongThanh/worktree-pg/internal/cluster"
	"github.com/LysCongThanh/worktree-pg/internal/envfile"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
	"github.com/LysCongThanh/worktree-pg/internal/runner"
	"github.com/LysCongThanh/worktree-pg/internal/template"
)

// echo is the live output sink for user commands (verbose mode only).
func (e *Engine) echo() io.Writer {
	if e.Opts.Verbose {
		return e.Opts.Stderr
	}
	return nil
}

// builder returns the template builder for the workspace on cluster cl.
func (e *Engine) builder(ws *workspace, cl *cluster.Cluster, a *pg.Admin, fp string, wait time.Duration) *template.Builder {
	b := &template.Builder{Admin: a, Cfg: ws.cfg, Dir: ws.repo.Toplevel, FP: fp, Host: ws.cfg.Database.Host,
		StateDir: e.Opts.StateDir, LogKey: ws.key, Wait: wait, Echo: e.echo(), Getenv: e.Opts.Getenv}
	if ws.perWorktree() {
		b.Owner, b.OwnerPwd = ws.owner, e.Secret.Password("role/"+cl.ID+"/"+ws.owner)
	}
	return b
}

// runSetup runs cluster_setup_cmd when its fingerprint is not yet recorded for
// this app on this cluster, then records it.
func (e *Engine) runSetup(ctx context.Context, ws *workspace, cl *cluster.Cluster, a *pg.Admin, wait time.Duration) (bool, error) {
	fp, err := template.SetupFingerprint(ws.cfg.Root, ws.cfg)
	if err != nil || fp == "" {
		return false, err
	}
	done := func() bool {
		ok := false
		_ = e.Store.View(func(st *registry.State) error {
			c := st.Clusters[cl.ID]
			ok = c != nil && c.Setup[ws.cfg.Name] != nil && c.Setup[ws.cfg.Name].FP == fp
			return nil
		})
		return ok
	}
	if done() {
		return false, nil
	}
	record := func() error {
		return e.Store.Update(ctx, func(st *registry.State) error {
			if c := st.Clusters[cl.ID]; c != nil {
				if c.Setup == nil {
					c.Setup = map[string]*registry.Setup{}
				}
				c.Setup[ws.cfg.Name] = &registry.Setup{FP: fp}
			}
			return nil
		})
	}
	s := &cluster.Setup{Admin: a, Cfg: ws.cfg, Dir: ws.repo.Toplevel, Host: ws.cfg.Database.Host,
		StateDir: e.Opts.StateDir, LogKey: ws.key, Wait: wait, Echo: e.echo(), Done: done, Record: record}
	return s.Run(ctx)
}

// templateFor returns the template the clone uses. A ready worktree whose marked
// database still exists keeps the template it was cloned from (no rebuild when
// inputs changed: status reports template_stale instead); otherwise the current
// template is ensured, built when missing.
func (e *Engine) templateFor(ctx context.Context, ws *workspace, admin *pg.Admin, b *template.Builder, w *registry.Worktree) (name, fp string, built bool, err error) {
	if w.Status == registry.StatusReady && w.Template != "" {
		d, err := admin.Database(ctx, ws.db)
		if err != nil {
			return "", "", false, err
		}
		if d != nil && names.Owns(d.Comment, names.KindClone, ws.key) {
			return w.Template, w.TemplateFP, false, nil
		}
	}
	name, built, err = b.Ensure(ctx)
	return name, b.FP, built, err
}

// cloneSpec is the backend spec of the workspace.
func (e *Engine) cloneSpec(ws *workspace, cl *cluster.Cluster, tpl, fp, password string) backend.Spec {
	var connect []string
	for _, d := range ws.cfg.DSN {
		if d.Role != pg.OwnerRef && d.Role != ws.role {
			connect = append(connect, d.Role)
		}
	}
	return backend.Spec{Key: ws.key, Path: ws.repo.Toplevel, App: ws.cfg.Name, Slug: ws.slug, DB: ws.db, Role: ws.role,
		OwnerRole: ws.owner, Template: tpl, RolesMode: ws.cfg.Database.Roles, Strategy: ws.cfg.Database.Strategy,
		RevokePublicConnect: ws.cfg.Database.RevokePublicConnect, ConnectRoles: connect, Password: password,
		Marker: names.Marker{Kind: names.KindClone, Key: ws.key, Path: ws.repo.Toplevel, FP: fp, Host: localHost()}}
}

// ensureClone runs the backend, re-ensuring the template once when it was
// dropped concurrently (spike S3).
func ensureClone(ctx context.Context, be backend.Backend, b *template.Builder, spec backend.Spec) (backend.Endpoint, error) {
	ep, err := be.Ensure(ctx, spec)
	if !errors.Is(err, pg.ErrTemplateGone) {
		return ep, err
	}
	if _, _, err := b.Ensure(ctx); err != nil {
		return ep, err
	}
	ep2, err := be.Ensure(ctx, spec)
	ep2.Actions = append(ep.Actions, ep2.Actions...)
	return ep2, err
}

// postClone runs post_clone_cmd as the worktree role on its fresh clone.
func (e *Engine) postClone(ctx context.Context, ws *workspace, cred pg.Cred) error {
	cmd := ws.cfg.Template.PostCloneCmd
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	return runner.Run(ctx, runner.Cmd{
		Label: "post_clone_cmd", Code: errs.CodePostCloneFailed, Script: cmd, Dir: ws.repo.Toplevel,
		Env: template.CommandEnv(ws.cfg.DSN, cred, e.Opts.Getenv), Strip: []string{ws.cfg.Cluster.AdminDSNEnv},
		LogPath: runner.LogPath(e.Opts.StateDir, ws.key, "post_clone_cmd", e.Opts.Now()),
		Echo:    e.echo(), Timeout: ws.cfg.Template.Timeout.Duration,
	})
}

// writeEnv splices vars into every [env].files entry (marker block only, S7).
// With on_duplicate = "error", keys also assigned outside the block abort
// before anything is written.
func (e *Engine) writeEnv(ws *workspace, vars []envfile.Var, prev []registry.EnvFile) ([]envfile.Result, []registry.EnvFile, []Warning, error) {
	created := map[string]bool{}
	for _, f := range prev {
		created[f.Path] = f.CreatedFile
	}
	var warns []Warning
	if ws.cfg.Env.OnDuplicate == "error" {
		for _, rel := range ws.cfg.Env.Files {
			old, _ := os.ReadFile(filepath.Join(ws.repo.Toplevel, rel))
			if _, dups, _ := envfile.Splice(old, vars); len(dups) > 0 {
				return nil, nil, nil, errs.New(errs.Config, errs.CodeConfigInvalid, "%s assigns %v outside the managed block", rel, dups).
					WithHint("remove them, or set [env] on_duplicate = \"warn\"")
			}
		}
	}
	results := make([]envfile.Result, 0, len(ws.cfg.Env.Files))
	recs := make([]registry.EnvFile, 0, len(ws.cfg.Env.Files))
	for _, rel := range ws.cfg.Env.Files {
		r, err := envfile.Write(ws.repo.Toplevel, rel, vars)
		if err != nil {
			return results, recs, warns, err
		}
		if len(r.DuplicateKeys) > 0 {
			warns = append(warns, Warning{Code: "ENV_DUPLICATE_KEYS", Message: rel + " also assigns " + strings.Join(r.DuplicateKeys, ", ") + " outside the managed block"})
		}
		results = append(results, r)
		recs = append(recs, registry.EnvFile{Path: rel, CreatedFile: created[rel] || r.Created})
	}
	return results, recs, warns, nil
}
