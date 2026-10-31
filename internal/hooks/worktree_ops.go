package hooks

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// LoadConfig loads the explicit config path or <dir>/.worktree-pg.toml. found is
// false (with defaults returned) when the repository has no config file: hooks
// then still manage the git worktree but skip provisioning.
func LoadConfig(dir, explicit string) (c *config.Config, found bool, err error) {
	if explicit != "" {
		c, err = config.Load(explicit)
		return c, err == nil, err
	}
	c, err = config.Find(dir)
	if errs.Is(err, errs.CodeConfigNotFound) {
		return config.Default(), false, nil
	}
	return c, err == nil, err
}

// Removal is a linked worktree verified safe to remove (invariant S9).
type Removal struct {
	Main     *gitx.Repo    // the main worktree, git commands run from there
	Worktree gitx.Worktree // the listed record of the target
}

// CheckRemovable verifies that path is a listed, linked (non-main), unlocked
// worktree. Anything else is a safety refusal (exit 2) and nothing is touched.
func CheckRemovable(ctx context.Context, path string) (*Removal, error) {
	repo, err := gitx.Open(ctx, path)
	if err != nil {
		return nil, refuse(path, "is not inside a git work tree")
	}
	list, err := repo.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	wt, idx, ok := gitx.Find(list, path)
	switch {
	case !ok:
		return nil, refuse(path, "is not listed by `git worktree list`")
	case idx == 0 || wt.Bare:
		return nil, refuse(path, "is the main worktree")
	case wt.Locked:
		return nil, refuse(path, "is locked ("+wt.LockReason+"); run `git worktree unlock` first")
	}
	main, err := gitx.Open(ctx, list[0].Path)
	if err != nil {
		return nil, err
	}
	return &Removal{Main: main, Worktree: wt}, nil
}

// Remove runs `git worktree remove [--force]` (never the double force that
// overrides a lock) and, when the hook created branch, `git branch -d` (merged
// only; failure is a warning). It never deletes directories itself.
func (r *Removal) Remove(ctx context.Context, force bool, branch string, stderr io.Writer) error {
	if err := r.Main.Remove(ctx, r.Worktree.Path, force); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "worktree-pg: removed worktree %s\n", r.Worktree.Path)
	if branch != "" {
		if err := r.Main.DeleteBranch(ctx, branch, false); err != nil {
			fmt.Fprintf(stderr, "worktree-pg: warning: kept branch %s (not fully merged)\n", branch)
		}
	}
	return nil
}

// FindEntry returns a copy of the registry entry whose path equals path (both
// symlink-resolved when possible), or nil.
func FindEntry(st *registry.Store, path string) (*registry.Worktree, error) {
	if st == nil {
		return nil, nil
	}
	want := resolve(path)
	var found *registry.Worktree
	err := st.View(func(s *registry.State) error {
		for _, w := range s.Worktrees {
			if resolve(w.Path) == want {
				cp := *w
				found = &cp
				return nil
			}
		}
		return nil
	})
	return found, err
}

// resolve cleans p and resolves symlinks when p exists.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func refuse(path, why string) error {
	return errs.New(errs.Conflict, errs.CodeNotRegistered, "%s %s", path, why).
		WithHint("worktree-pg only removes registered, unlocked, non-main worktrees")
}

// exists reports whether anything (file, dir, symlink) is at p.
func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// isDir reports whether p exists and is a directory.
func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
