package engine

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// workspace is a worktree resolved from a path: repository, config and every
// derived name (spec §5 "Naming"). It is computed without touching the cluster.
type workspace struct {
	repo   *gitx.Repo
	cfg    *config.Config
	key    string
	slug   string
	branch string
	db     string
	role   string // per_worktree: == db; shared: the admin user (set after connect)
	owner  string // per_worktree: <app>_wt_owner; shared: the admin user
}

// absPath resolves p against Opts.Cwd ("" = Cwd).
func (e *Engine) absPath(p string) string {
	if p == "" {
		return e.Opts.Cwd
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.Opts.Cwd, p)
	}
	return filepath.Clean(p)
}

// workspace opens the git worktree containing path and loads its config
// (Opts.ConfigPath, else <toplevel>/.worktree-pg.toml).
func (e *Engine) workspace(ctx context.Context, path string) (*workspace, error) {
	repo, err := gitx.Open(ctx, e.absPath(path))
	if err != nil {
		return nil, err
	}
	var cfg *config.Config
	if e.Opts.ConfigPath != "" {
		cfg, err = config.Load(e.absPath(e.Opts.ConfigPath))
	} else {
		cfg, err = config.Find(repo.Toplevel)
	}
	if err != nil {
		return nil, err
	}
	key, slug := names.Key(repo.Toplevel), names.Slug(repo.Toplevel)
	db := names.Database(cfg.Name, slug, key)
	ws := &workspace{repo: repo, cfg: cfg, key: key, slug: slug, branch: repo.CurrentBranch(ctx), db: db, role: db, owner: names.OwnerRole(cfg.Name)}
	return ws, nil
}

// perWorktree reports whether each worktree gets its own role.
func (w *workspace) perWorktree() bool { return w.cfg.Database.Roles == "per_worktree" }

// useAdminRole switches shared roles mode to the admin identity.
func (w *workspace) useAdminRole(adminUser string) {
	if !w.perWorktree() {
		w.role, w.owner = adminUser, adminUser
	}
}

// ref is the JSON identity of the worktree.
func (w *workspace) ref() WorktreeRef {
	return WorktreeRef{Path: w.repo.Toplevel, Key: w.key, Slug: w.slug, Branch: w.branch}
}

// exists reports whether a path exists (Lstat).
func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// configFor loads the config governing a registered worktree, even when its
// directory is gone: the worktree's own file, else the main worktree's (next to
// the shared git dir), else defaults named after the recorded app.
func (e *Engine) configFor(w *registry.Worktree) *config.Config {
	if e.Opts.ConfigPath != "" {
		if c, err := config.Load(e.absPath(e.Opts.ConfigPath)); err == nil {
			return c
		}
	}
	for _, dir := range []string{w.Path, filepath.Dir(w.RepoCommonDir)} {
		if dir == "" || !exists(filepath.Join(dir, config.FileName)) {
			continue
		}
		if c, err := config.Find(dir); err == nil {
			return c
		}
	}
	c := config.Default()
	c.Name = w.App
	return c
}
