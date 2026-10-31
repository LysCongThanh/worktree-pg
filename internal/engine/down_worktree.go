package engine

import (
	"context"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
)

// checkRemovable verifies, before anything is dropped, that path may be removed
// with `git worktree remove` (invariant S9): it must be listed by git, not the
// main worktree and not locked. It returns the main worktree's repo to run the
// removal from. A missing directory needs no removal (nil repo, nil error).
func checkRemovable(ctx context.Context, path string) (*gitx.Repo, error) {
	if !exists(path) {
		return nil, nil
	}
	repo, err := gitx.Open(ctx, path)
	if err != nil {
		return nil, refuseRemove(path, "it is not inside a git worktree")
	}
	list, err := repo.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	wt, idx, ok := gitx.Find(list, path)
	switch {
	case !ok:
		return nil, refuseRemove(path, "git does not list it as a worktree")
	case idx == 0:
		return nil, refuseRemove(path, "it is the main worktree")
	case wt.Locked:
		return nil, refuseRemove(path, "it is locked ("+wt.LockReason+")")
	}
	return gitx.Open(ctx, list[0].Path)
}

func refuseRemove(path, why string) error {
	return errs.New(errs.Conflict, errs.CodeNotRegistered, "refusing to remove %s: %s", path, why).
		WithHint("nothing was changed; remove the directory yourself if intended")
}
