package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
)

const createPayload = `{"session_id":"s1","transcript_path":"/t","cwd":"/ignored","hook_event_name":"WorktreeCreate","name":"feat-x","future":1}`

// TestClaudeCreate_SuccessPrintsPathLast: worktree created on the configured
// branch, .worktreeinclude files copied, Up called with origin claude-hook, and
// the LAST stdout line is the clean real path (all chatter on stderr).
func TestClaudeCreate_SuccessPrintsPathLast(t *testing.T) {
	root := newRepo(t, minimalConfig)
	write(t, filepath.Join(root, ".env"), "SECRET=1\n")
	write(t, filepath.Join(root, ".worktreeinclude"), ".env\n")
	rec := &recorder{}
	env, out, errb := hookEnv(root, createPayload, rec, nil)
	if err := ClaudeCreate(context.Background(), env); err != nil {
		t.Fatalf("%v\nstderr: %s", err, errb)
	}
	want := filepath.Join(root, ".claude", "worktrees", "feat-x")
	if out.String() != want+"\n" {
		t.Fatalf("stdout must be exactly the path, got %q", out.String())
	}
	if b, _ := os.ReadFile(filepath.Join(want, ".env")); string(b) != "SECRET=1\n" {
		t.Fatal(".worktreeinclude file not copied")
	}
	if got := git(t, want, "branch", "--show-current"); got != "worktree-feat-x" {
		t.Fatalf("branch %q", got)
	}
	if len(rec.ups) != 1 || rec.ups[0].Path != want || rec.ups[0].Origin != "claude-hook" || rec.ups[0].Wait <= 0 {
		t.Fatalf("up requests %+v", rec.ups)
	}
	// re-running with the same name reuses the worktree (idempotent)
	env, out, _ = hookEnv(root, createPayload, rec, nil)
	if err := ClaudeCreate(context.Background(), env); err != nil || lastLine(out.String()) != want {
		t.Fatalf("reuse: %v %q", err, out)
	}
}

// TestClaudeCreate_RollbackOnUpFailure uses the real engine without an admin DSN:
// Up fails, so the worktree dir and the branch created by this run disappear and
// nothing is printed on stdout.
func TestClaudeCreate_RollbackOnUpFailure(t *testing.T) {
	root := newRepo(t, minimalConfig)
	e := realEngine(t, root)
	env, out, errb := hookEnv(root, createPayload, e, e)
	err := ClaudeCreate(context.Background(), env)
	if err == nil || errs.ExitCode(err) == 0 {
		t.Fatal("expected provisioning failure")
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must stay empty on failure: %q", out)
	}
	if _, serr := os.Stat(filepath.Join(root, ".claude", "worktrees", "feat-x")); !os.IsNotExist(serr) {
		t.Fatal("worktree dir not rolled back")
	}
	repo, _ := gitx.Open(context.Background(), root)
	if repo.BranchExists(context.Background(), "worktree-feat-x") {
		t.Fatal("created branch not deleted")
	}
	if !strings.Contains(errb.String(), "rolling back") {
		t.Fatalf("stderr: %s", errb)
	}
}

// TestClaudeCreate_KeepOnUpFailure: on_up_failure = "keep" prints the path anyway.
func TestClaudeCreate_KeepOnUpFailure(t *testing.T) {
	root := newRepo(t, minimalConfig+"[hooks.claude]\non_up_failure = \"keep\"\n")
	e := realEngine(t, root)
	env, out, _ := hookEnv(root, createPayload, e, e)
	if err := ClaudeCreate(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if p := lastLine(out.String()); !isDir(p) {
		t.Fatalf("kept worktree missing: %q", p)
	}
}

// TestClaudeCreate_NoConfig still creates the worktree (Claude replaced its own
// creation with this hook) but never calls Up; an existing branch is reused.
func TestClaudeCreate_NoConfig(t *testing.T) {
	root := newRepo(t, "")
	git(t, root, "branch", "worktree-feat-x")
	rec := &recorder{}
	env, out, errb := hookEnv(root, createPayload, rec, nil)
	if err := ClaudeCreate(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if len(rec.ups) != 0 || !isDir(lastLine(out.String())) || !strings.Contains(errb.String(), "without a database") {
		t.Fatalf("ups=%d out=%q err=%q", len(rec.ups), out, errb)
	}
}

// TestClaudeCreate_Refusals: invalid names and a foreign directory at the target.
func TestClaudeCreate_Refusals(t *testing.T) {
	root := newRepo(t, "")
	for _, payload := range []string{`{"name":"../x"}`, `{"name":"a/b"}`, `{"name":""}`, `not json`, ``} {
		env, out, _ := hookEnv(root, payload, &recorder{}, nil)
		if err := ClaudeCreate(context.Background(), env); errs.ExitCode(err) != 3 || out.Len() != 0 {
			t.Errorf("%q: %v", payload, err)
		}
	}
	foreign := filepath.Join(root, ".claude", "worktrees", "feat-x")
	write(t, filepath.Join(foreign, "mine.txt"), "keep me\n")
	env, _, _ := hookEnv(root, createPayload, &recorder{}, nil)
	if err := ClaudeCreate(context.Background(), env); errs.CodeOf(err) != errs.CodeNotRegistered || errs.ExitCode(err) != 2 {
		t.Fatalf("foreign dir: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(foreign, "mine.txt")); string(b) != "keep me\n" {
		t.Fatal("foreign dir touched")
	}
}
