//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2E_UpEnvRunDown: up is idempotent (second run has no actions), `run`
// execs with the worktree variables, down drops everything and env then exits 7.
func TestE2E_UpEnvRunDown(t *testing.T) {
	if os.Getenv("WTPG_TEST_ADMIN_DSN") == "" {
		t.Skip("WTPG_TEST_ADMIN_DSN not set")
	}
	s := newSandbox(t, true)
	root := s.repo(fixture(t))
	wt := filepath.Join(s.dir, "shop-feat")
	if r := s.run(root, "", "git", "worktree", "add", "-q", "-b", "feat", wt); r.code != 0 {
		t.Fatal(r.stderr)
	}
	r := s.run(wt, "", "", "--json", "up")
	skipIfNotImplemented(t, r)
	if r.code != 0 || envelope(t, r)["ok"] != true {
		t.Fatalf("up: %+v", r)
	}
	if strings.Contains(r.stdout, "password") {
		t.Fatal("up JSON must not carry passwords (S6)")
	}
	r = s.run(wt, "", "", "--json", "up")
	actions := envelope(t, r)["result"].(map[string]any)["actions"]
	if a, _ := actions.([]any); r.code != 0 || len(a) != 0 {
		t.Fatalf("second up not idempotent: %v", actions)
	}
	r = s.run(wt, "", "", "run", "--", "sh", "-c", `psql -X -Atc "select count(*) from items" "$DATABASE_URL"`)
	if r.code != 0 || strings.TrimSpace(r.stdout) != "1" {
		t.Fatalf("run: %+v", r)
	}
	if r := s.run(wt, "", "", "env", "--format", "shell"); r.code != 0 || !strings.Contains(r.stdout, "export DATABASE_URL=") {
		t.Fatalf("env shell: %+v", r)
	}
	if r := s.run(wt, "", "", "down"); r.code != 0 {
		t.Fatalf("down: %+v", r)
	}
	if r := s.run(wt, "", "", "env"); r.code != 7 {
		t.Fatalf("env after down must exit 7: %+v", r)
	}
}

// TestE2E_GCAfterRmRf: a worktree deleted with rm -rf + prune is listed by the
// gc dry run and dropped with --yes.
func TestE2E_GCAfterRmRf(t *testing.T) {
	if os.Getenv("WTPG_TEST_ADMIN_DSN") == "" {
		t.Skip("WTPG_TEST_ADMIN_DSN not set")
	}
	s := newSandbox(t, true)
	root := s.repo(fixture(t))
	wt := filepath.Join(s.dir, "shop-gone")
	s.run(root, "", "git", "worktree", "add", "-q", "-b", "gone", wt)
	r := s.run(wt, "", "", "up")
	skipIfNotImplemented(t, r)
	if r.code != 0 {
		t.Fatalf("up: %+v", r)
	}
	_ = os.RemoveAll(wt) // test fixture under t.TempDir, simulating a user's rm -rf
	s.run(root, "", "git", "worktree", "prune")
	r = s.run(root, "", "", "--json", "gc")
	skipIfNotImplemented(t, r)
	if !strings.Contains(r.stdout, `"would_drop"`) {
		t.Fatalf("dry run: %+v", r)
	}
	if r = s.run(root, "", "", "--json", "gc", "--yes"); r.code != 0 || !strings.Contains(r.stdout, `"dropped"`) {
		t.Fatalf("gc --yes: %+v", r)
	}
}
