package gitx

import "context"

// Add runs `git worktree add [-b branch] <path> <base>`. When newBranch is false
// the existing branch is checked out instead. env lets callers set
// WORKTREE_PG_IN_HOOK=1 so the post-checkout hook does not recurse.
func (r *Repo) Add(ctx context.Context, path, branch, base string, newBranch bool, env []string) error {
	args := []string{"worktree", "add"}
	if newBranch {
		args = append(args, "-b", branch, path, base)
	} else {
		args = append(args, path, branch)
	}
	_, err := Git(ctx, r.Toplevel, env, args...)
	return err
}

// Remove runs `git worktree remove [--force] <path>`. A single --force discards
// untracked/modified files but still refuses locked worktrees; worktree-pg never
// passes the double force that would override a lock (invariant S9, spike S9:
// ignored files such as .env never block removal).
func (r *Repo) Remove(ctx context.Context, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	_, err := Git(ctx, r.Toplevel, nil, append(args, path)...)
	return err
}

// DeleteBranch deletes a local branch: `-d` (merged only) unless force (`-D`).
func (r *Repo) DeleteBranch(ctx context.Context, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := Git(ctx, r.Toplevel, nil, "branch", flag, branch)
	return err
}

// Prune runs `git worktree prune` (drops records of deleted worktree dirs).
func (r *Repo) Prune(ctx context.Context) error {
	_, err := Git(ctx, r.Toplevel, nil, "worktree", "prune")
	return err
}
