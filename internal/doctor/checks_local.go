package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
	"github.com/LysCongThanh/worktree-pg/internal/hooks"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
	"github.com/LysCongThanh/worktree-pg/internal/secret"
)

// checkGit verifies git is on PATH (porcelain -z needs git >= 2.36).
func (r *run) checkGit(ctx context.Context) {
	out, err := exec.CommandContext(ctx, "git", "version").Output()
	if err != nil {
		r.add("git", Fail, "git not found on PATH", "install git >= 2.36")
		return
	}
	r.add("git", OK, strings.TrimSpace(string(out)), "")
}

// checkRepoAndConfig finds the repository and loads/validates its config.
func (r *run) checkRepoAndConfig(ctx context.Context) {
	repo, err := gitx.Open(ctx, r.o.Cwd)
	if err != nil {
		r.add("repository", Fail, err.Error(), "run doctor inside a git work tree")
		return
	}
	r.root = repo.Toplevel
	r.add("repository", OK, repo.Toplevel, "")
	if r.o.ConfigPath != "" {
		r.cfg, r.cfgErr = config.Load(r.o.ConfigPath)
	} else {
		r.cfg, r.cfgErr = config.Find(repo.Toplevel)
	}
	if r.cfgErr != nil {
		r.cfg = nil
		r.add("config", Fail, r.cfgErr.Error(), hintOf(r.cfgErr))
		return
	}
	r.add("config", OK, fmt.Sprintf("%s (app %q, cluster %s, roles %s)", r.cfg.Path, r.cfg.Name, r.cfg.Cluster.Source, r.cfg.Database.Roles), "")
}

// checkState inspects the state directory, secret key and registry.
func (r *run) checkState() {
	dir := r.o.StateDir
	st, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		r.add("state", OK, dir+" (created on first up)", "")
		return
	case err != nil:
		r.add("state", Fail, err.Error(), "")
		return
	case st.Mode().Perm()&0o077 != 0:
		r.add("state", Warn, fmt.Sprintf("%s has mode %o", dir, st.Mode().Perm()), "chmod 700 "+dir)
	default:
		r.add("state", OK, dir, "")
	}
	if ks, err := os.Stat(filepath.Join(dir, secret.FileName)); err == nil {
		if ks.Mode().Perm()&0o077 != 0 {
			r.add("secret key", Fail, fmt.Sprintf("%s has mode %o", secret.FileName, ks.Mode().Perm()),
				"chmod 600 "+filepath.Join(dir, secret.FileName))
		} else {
			r.add("secret key", OK, "mode 0600", "")
		}
	}
	store, err := registry.Open(dir)
	if err == nil {
		n := 0
		err = store.View(func(s *registry.State) error { n = len(s.Worktrees); return nil })
		if err == nil {
			r.add("registry", OK, fmt.Sprintf("%d worktree(s) registered", n), "")
			return
		}
	}
	r.add("registry", Fail, err.Error(), hintOf(err))
}

// checkDocker requires a reachable docker daemon for managed clusters only.
func (r *run) checkDocker(ctx context.Context) {
	if r.cfg == nil || r.cfg.Cluster.Source != "docker" {
		r.add("docker", Skip, "not needed (external cluster)", "")
		return
	}
	cctx, cancel := context.WithTimeout(ctx, r.o.Timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		r.add("docker", Fail, "docker daemon not reachable", "start docker, or use [cluster] source = \"external\"")
		return
	}
	r.add("docker", OK, "server "+strings.TrimSpace(string(out)), "")
}

// checkHooks reports whether the Claude Code and git hooks are installed (warn only).
func (r *run) checkHooks(ctx context.Context) {
	if r.root == "" {
		return
	}
	found := ""
	for _, scope := range []string{"local", "project", "user"} {
		p, _ := hooks.ClaudeSettingsPath(scope, r.root, r.o.Home)
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), hooks.ClaudeCreateCmd) {
			found = p
			break
		}
	}
	if found != "" {
		r.add("claude hook", OK, found, "")
	} else {
		r.add("claude hook", Warn, "not installed", "worktree-pg hook install claude")
	}
	p, err := hooks.GitHookPath(ctx, r.root)
	b, _ := os.ReadFile(p)
	switch {
	case strings.Contains(string(b), "worktree-pg git-hook post-checkout"):
		r.add("git hook", OK, p, "")
	case err != nil:
		r.add("git hook", Warn, err.Error(), hintOf(err))
	default:
		r.add("git hook", Warn, "post-checkout not installed", "worktree-pg hook install git")
	}
}
