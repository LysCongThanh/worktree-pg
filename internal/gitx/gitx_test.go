package gitx

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

func TestParsePorcelainZGolden(t *testing.T) {
	b, err := os.ReadFile("testdata/porcelain_git255.z")
	if err != nil {
		t.Fatal(err)
	}
	list, err := ParsePorcelainZ(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 6 {
		t.Fatalf("want 6 worktrees, got %d: %+v", len(list), list)
	}
	if list[0].Path != "/home/dev/shop/repo" || list[0].Branch != "main" || len(list[0].HEAD) != 40 {
		t.Fatalf("main: %+v", list[0])
	}
	if w := list[2]; w.Path != "/home/dev/shop/s9 sp/x" || !w.Prunable || !strings.Contains(w.PrunableReason, "non-existent") {
		t.Fatalf("prunable with space: %+v", w)
	}
	if w := list[4]; !w.Locked || w.LockReason != "agent busy" || w.Branch != "s9c" {
		t.Fatalf("locked: %+v", w)
	}
	if w := list[5]; !w.Detached || w.Branch != "" {
		t.Fatalf("detached: %+v", w)
	}
}

func TestParsePorcelainZEdgeCases(t *testing.T) {
	list, err := ParsePorcelainZ([]byte("worktree /r\x00bare\x00\x00worktree /a\nb\x00HEAD x\x00locked\x00future-attr 1\x00\x00"))
	if err != nil || len(list) != 2 || !list[0].Bare || list[1].Path != "/a\nb" || !list[1].Locked || list[1].LockReason != "" {
		t.Fatalf("%+v %v", list, err)
	}
	if l, _ := ParsePorcelainZ([]byte("worktree /x\x00HEAD y")); len(l) != 1 { // missing terminator tolerated
		t.Fatal("unterminated last record")
	}
	for _, bad := range []string{"HEAD x\x00\x00", "worktree /a\x00worktree /b\x00\x00"} {
		if _, err := ParsePorcelainZ([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if l, err := ParsePorcelainZ(nil); err != nil || len(l) != 0 {
		t.Fatal("empty input")
	}
}

// newRepo creates an isolated repo with one commit, ignoring global git config.
func newRepo(t *testing.T) string {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		t.Setenv(k, v)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(dir, "shop")
	_ = os.Mkdir(root, 0o755)
	ctx := context.Background()
	must := func(args ...string) {
		if _, err := Git(ctx, root, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	must("init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".env\nnode_modules/\n*.local\n"), 0o644)
	must("add", ".gitignore")
	must("commit", "-q", "-m", "init")
	return root
}

func TestRepoWorktreeLifecycle(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t)
	r, err := Open(ctx, filepath.Join(root))
	if err != nil {
		t.Fatal(err)
	}
	if r.Toplevel != root || !r.IsMain() || r.CurrentBranch(ctx) != "main" || r.DefaultBase(ctx) != "HEAD" {
		t.Fatalf("repo: %+v branch=%q", r, r.CurrentBranch(ctx))
	}
	wt := filepath.Join(filepath.Dir(root), "shop-a")
	if err := r.Add(ctx, wt, "feat-a", "HEAD", true, []string{"WORKTREE_PG_IN_HOOK=1"}); err != nil {
		t.Fatal(err)
	}
	if !r.BranchExists(ctx, "feat-a") || r.BranchExists(ctx, "nope") {
		t.Fatal("BranchExists")
	}
	linked, _ := Open(ctx, wt)
	if linked.IsMain() || linked.CommonDir != r.CommonDir {
		t.Fatalf("linked: %+v", linked)
	}
	list, _ := r.Worktrees(ctx)
	if w, i, ok := Find(list, wt); !ok || i != 1 || w.Branch != "feat-a" {
		t.Fatalf("Find: %+v %d %v", w, i, ok)
	}
	// ignored files never block a plain remove (spike S9)
	_ = os.WriteFile(filepath.Join(wt, ".env"), []byte("X=1\n"), 0o600)
	if err := r.Remove(ctx, wt, false); err != nil {
		t.Fatalf("remove with ignored file: %v", err)
	}
	if err := r.DeleteBranch(ctx, "feat-a", false); err != nil {
		t.Fatal(err)
	}
	// locked worktrees are refused even with a single --force (invariant S9)
	wt2 := filepath.Join(filepath.Dir(root), "shop-b")
	_ = r.Add(ctx, wt2, "feat-b", "HEAD", true, nil)
	_, _ = Git(ctx, root, nil, "worktree", "lock", wt2)
	if err := r.Remove(ctx, wt2, true); errs.CodeOf(err) != errs.CodeGitFailed {
		t.Fatalf("locked worktree removed: %v", err)
	}
	if _, err := os.Stat(wt2); err != nil {
		t.Fatal("locked worktree dir gone")
	}
	_ = r.Add(ctx, filepath.Join(filepath.Dir(root), "shop-c"), "feat-b2", "", false, nil)
	if err := r.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, t.TempDir()); errs.CodeOf(err) != errs.CodeNotGitRepo || errs.ExitCode(err) != 3 {
		t.Fatalf("non-repo: %v", err)
	}
}

func TestCopyWorktreeInclude(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t)
	dst := t.TempDir()
	if got, err := CopyWorktreeInclude(ctx, root, dst); err != nil || got != nil {
		t.Fatal("no include file should be a no-op")
	}
	files := map[string]string{
		".env": "SECRET=1\n", "config/app.local": "a\n", "node_modules/x/i.js": "x\n",
		"notes.txt": "untracked, not ignored\n", "keep.local": "dst has it\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o640)
		_ = os.Chmod(p, 0o640)
	}
	_ = os.Symlink("/etc/passwd", filepath.Join(root, "link.local"))
	_ = os.WriteFile(filepath.Join(root, IncludeFile), []byte(".env\n*.local\nnotes.txt\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dst, "keep.local"), []byte("mine\n"), 0o644)
	got, err := CopyWorktreeInclude(ctx, root, dst)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != ".env,config/app.local" {
		t.Fatalf("copied %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "keep.local")); string(b) != "mine\n" {
		t.Fatal("overwrote existing file")
	}
	if st, _ := os.Stat(filepath.Join(dst, ".env")); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode not preserved: %o", st.Mode().Perm())
	}
	for _, absent := range []string{"notes.txt", "link.local", "node_modules/x/i.js"} {
		if _, err := os.Lstat(filepath.Join(dst, absent)); err == nil {
			t.Errorf("%s should not be copied", absent)
		}
	}
}
