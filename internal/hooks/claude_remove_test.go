package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

func removePayload(path string) string {
	return fmt.Sprintf(`{"hook_event_name":"WorktreeRemove","worktree_path":%q}`, path)
}

// TestClaudeRemove_RemovesLinkedWorktreeAndCreatedBranch: Down is called for the
// registered entry, the dir goes via `git worktree remove`, the merged branch the
// hook created is deleted.
func TestClaudeRemove_RemovesLinkedWorktreeAndCreatedBranch(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, "")
	wt := filepath.Join(root, ".claude", "worktrees", "a")
	git(t, root, "worktree", "add", "-q", "-b", "worktree-a", wt)
	write(t, filepath.Join(wt, "untracked.txt"), "x\n") // remove_force default true
	e := realEngine(t, root)
	key := names.Key(wt)
	_ = e.Store.Update(ctx, func(s *registry.State) error {
		s.Worktrees[key] = &registry.Worktree{Key: key, Path: wt, CreatedBranch: "worktree-a"}
		return nil
	})
	rec := &recorder{}
	env, out, errb := hookEnv(root, removePayload(wt), rec, e)
	if err := ClaudeRemove(ctx, env); err != nil {
		t.Fatalf("%v\n%s", err, errb)
	}
	if len(rec.downs) != 1 || rec.downs[0].Key != key || out.Len() != 0 {
		t.Fatalf("downs=%+v out=%q", rec.downs, out)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("worktree dir still present")
	}
	if git(t, root, "branch", "--list", "worktree-a") != "" {
		t.Fatal("created branch kept")
	}
}

// TestClaudeRemove_GoneDirectory: a deleted dir that is not registered is a no-op;
// a registered one is torn down by key.
func TestClaudeRemove_GoneDirectory(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, "")
	gone := filepath.Join(root, "nope")
	e := realEngine(t, root)
	rec := &recorder{}
	env, _, _ := hookEnv(root, removePayload(gone), rec, e)
	if err := ClaudeRemove(ctx, env); err != nil || len(rec.downs) != 0 {
		t.Fatalf("%v %+v", err, rec.downs)
	}
	_ = e.Store.Update(ctx, func(s *registry.State) error {
		s.Worktrees["k1"] = &registry.Worktree{Key: "k1", Path: gone}
		return nil
	})
	env, _, _ = hookEnv(root, removePayload(gone), rec, e)
	if err := ClaudeRemove(ctx, env); err != nil || len(rec.downs) != 1 || rec.downs[0].Key != "k1" {
		t.Fatalf("%v %+v", err, rec.downs)
	}
}

// TestInvariant_S9_RemoveRefusals: main, locked, unlisted and relative paths are
// refused with exit 2 (or 3 for bad input) and stay on disk; Down is not called.
func TestInvariant_S9_RemoveRefusals(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, "")
	locked := filepath.Join(root, ".claude", "worktrees", "locked")
	git(t, root, "worktree", "add", "-q", "-b", "l", locked)
	git(t, root, "worktree", "lock", locked)
	plain := filepath.Join(root, "plain")
	write(t, filepath.Join(plain, "f"), "x")
	for path, code := range map[string]int{root: 2, locked: 2, plain: 2, "rel/path": 3} {
		rec := &recorder{}
		env, _, _ := hookEnv(root, removePayload(path), rec, nil)
		err := ClaudeRemove(ctx, env)
		if errs.ExitCode(err) != code || len(rec.downs) != 0 {
			t.Errorf("%s: exit %d (%v), downs %d", path, errs.ExitCode(err), err, len(rec.downs))
		}
		if filepath.IsAbs(path) && !isDir(path) {
			t.Errorf("%s removed", path)
		}
	}
}
