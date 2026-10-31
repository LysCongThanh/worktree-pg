package engine

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/LysCongThanh/worktree-pg/internal/cluster"
	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/envfile"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// Env returns the variables of an already provisioned worktree from the registry;
// it never provisions or connects (NOT_PROVISIONED, exit 7).
func (e *Engine) Env(ctx context.Context, path string) ([]envfile.Var, error) {
	ws, err := e.workspace(ctx, path)
	if err != nil {
		return nil, err
	}
	var w registry.Worktree
	var c registry.Cluster
	err = e.Store.View(func(st *registry.State) error {
		got := st.Worktrees[ws.key]
		if got == nil || got.Status != registry.StatusReady || st.Clusters[got.Cluster] == nil {
			return errs.New(errs.NotFound, errs.CodeNotProvisioned, "%s is not provisioned", ws.repo.Toplevel).
				WithHint("run `worktree-pg up` first")
		}
		w, c = *got, *st.Clusters[got.Cluster]
		return nil
	})
	if err != nil {
		return nil, err
	}
	pw, err := e.rolePassword(ws.cfg, &c, w.Role)
	if err != nil {
		return nil, err
	}
	return renderVars(ws.cfg, varData{App: w.App, Slug: ws.slug, Key: w.Key, Branch: ws.branch, Path: w.Path},
		w.Ports, pg.Cred{Host: ws.cfg.Database.Host, Port: c.Port, User: w.Role, Password: pw, Database: w.DB}, e.Opts.Getenv)
}

// rolePassword returns the password of the role a worktree connects as: the
// HMAC-derived one for per-worktree roles, else the admin's (shared mode).
func (e *Engine) rolePassword(cfg *config.Config, c *registry.Cluster, role string) (string, error) {
	if cfg.Database.Roles == "per_worktree" {
		return e.Secret.Password("role/" + c.ID + "/" + role), nil
	}
	if c.Kind == cluster.KindDocker {
		return e.Secret.Password("cluster/" + c.Container), nil
	}
	ext, err := cluster.External(cfg, e.Opts.Getenv)
	if err != nil {
		return "", err
	}
	return ext.AdminPassword(), nil
}

// varData is the dot of [env.vars] templates.
type varData struct {
	App, Slug, Key, Branch, Path string
}

// renderVars builds the managed variables in a stable order: [[dsn]] entries (in
// config order), [ports.vars] (sorted), then [env.vars] (sorted) rendered with
// text/template plus Port "X" and DSN "VAR". Values embedding a password are secret.
func renderVars(cfg *config.Config, d varData, ports map[string]int, cred pg.Cred, getenv func(string) string) ([]envfile.Var, error) {
	vars := pg.DSNVars(cfg.DSN, cred, getenv)
	dsn := map[string]string{}
	for _, v := range vars {
		dsn[v.Key] = v.Value
	}
	pv := make([]string, 0, len(cfg.Ports.Vars))
	for k := range cfg.Ports.Vars {
		pv = append(pv, k)
	}
	sort.Strings(pv)
	for _, k := range pv {
		vars = append(vars, envfile.Var{Key: k, Value: strconv.Itoa(ports[k])})
	}
	funcs := template.FuncMap{
		"Port": func(name string) (int, error) {
			if p, ok := ports[name]; ok {
				return p, nil
			}
			return 0, errs.New(errs.Config, errs.CodeConfigInvalid, "Port %q is not in [ports.vars]", name)
		},
		"DSN": func(name string) (string, error) {
			if v, ok := dsn[name]; ok {
				return v, nil
			}
			return "", errs.New(errs.Config, errs.CodeConfigInvalid, "DSN %q is not a [[dsn]] var", name)
		},
	}
	keys := make([]string, 0, len(cfg.Env.Vars))
	for k := range cfg.Env.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t, err := template.New(k).Funcs(funcs).Option("missingkey=error").Parse(cfg.Env.Vars[k])
		if err != nil {
			return nil, errs.Wrap(errs.Config, errs.CodeConfigInvalid, err, "env.vars.%s", k)
		}
		var b strings.Builder
		if err := t.Execute(&b, d); err != nil {
			return nil, errs.Wrap(errs.Config, errs.CodeConfigInvalid, err, "render env.vars.%s", k)
		}
		v := b.String()
		vars = append(vars, envfile.Var{Key: k, Value: v, Secret: cred.Password != "" && strings.Contains(v, cred.Password)})
	}
	return vars, nil
}
