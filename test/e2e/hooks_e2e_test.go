//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture returns the "shop" project files. With a test server configured, the
// app name is unique per test and its leftovers (template, owner role) are
// dropped on cleanup, so tests never share objects on the cluster.
func fixture(t *testing.T) map[string]string {
	app := "shop"
	if os.Getenv("WTPG_TEST_ADMIN_DSN") != "" {
		app = newApp(t)
	}
	return map[string]string{
		".worktree-pg.toml": strings.Replace(config, `name = "shop"`, fmt.Sprintf("name = %q", app), 1), "db/schema.sql": schema, ".gitignore": ".env\n.claude/\n",
	}
}

func TestE2E_VersionAndUsage(t *testing.T) {
	s := newSandbox(t, false)
	if r := s.run(s.dir, "", "", "--json", "version"); r.code != 0 || envelope(t, r)["ok"] != true {
		t.Fatalf("%+v", r)
	}
	if r := s.run(s.dir, "", "", "bogus"); r.code != 3 || r.stdout != "" {
		t.Fatalf("%+v", r)
	}
}

// TestE2E_GitHookNeverBreaksCheckout: with no admin DSN, `git worktree add` still
// succeeds; the hook explains the failure on stderr.
func TestE2E_GitHookNeverBreaksCheckout(t *testing.T) {
	s := newSandbox(t, false)
	root := s.repo(fixture(t))
	if r := s.run(root, "", "", "hook", "install", "git"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	wt := filepath.Join(s.dir, "shop-feat")
	r := s.run(root, "", "git", "worktree", "add", "-q", "-b", "feat", wt)
	if r.code != 0 || !strings.Contains(r.stderr, "worktree-pg") {
		t.Fatalf("git worktree add: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(wt, ".worktree-pg.toml")); err != nil {
		t.Fatal("checkout incomplete")
	}
}

// TestE2E_ClaudeHookRollbackWithoutDSN: create fails (exit != 0), stdout stays
// empty and neither the worktree nor the branch survives.
func TestE2E_ClaudeHookRollbackWithoutDSN(t *testing.T) {
	s := newSandbox(t, false)
	root := s.repo(fixture(t))
	r := s.run(root, `{"hook_event_name":"WorktreeCreate","name":"agent-1"}`, "", "claude-hook", "create")
	if r.code == 0 || r.stdout != "" {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "worktrees", "agent-1")); !os.IsNotExist(err) {
		t.Fatal("worktree not rolled back")
	}
	if b := s.run(root, "", "git", "branch", "--list", "worktree-agent-1"); strings.TrimSpace(b.stdout) != "" {
		t.Fatal("branch not rolled back")
	}
}

// TestE2E_ClaudeHookLifecycle drives the full Claude Code flow against a real
// server: create (provision) → env/status → psql through DATABASE_URL → remove.
func TestE2E_ClaudeHookLifecycle(t *testing.T) {
	if os.Getenv("WTPG_TEST_ADMIN_DSN") == "" {
		t.Skip("WTPG_TEST_ADMIN_DSN not set")
	}
	s := newSandbox(t, true)
	root := s.repo(fixture(t))
	r := s.run(root, `{"session_id":"e2e","cwd":"`+root+`","hook_event_name":"WorktreeCreate","name":"agent-1"}`, "", "claude-hook", "create")
	skipIfNotImplemented(t, r)
	if r.code != 0 {
		t.Fatalf("create: %+v", r)
	}
	wt := lastLine(r.stdout)
	if wt != filepath.Join(root, ".claude", "worktrees", "agent-1") || strings.Count(r.stdout, "\n") != 1 {
		t.Fatalf("stdout must be only the path: %q", r.stdout)
	}
	env := s.run(wt, "", "", "env", "--format", "json")
	vars := map[string]string{}
	for _, v := range envelope(t, env)["result"].(map[string]any)["vars"].([]any) {
		m := v.(map[string]any)
		vars[m["key"].(string)] = m["value"].(string)
	}
	if vars["DATABASE_URL"] == "" || vars["PORT"] == "" {
		t.Fatalf("vars: %v", vars)
	}
	if q := s.run(wt, "", "psql", "-X", "-Atc", "select name from items", vars["DATABASE_URL"]); q.code != 0 || strings.TrimSpace(q.stdout) != "seed" {
		t.Fatalf("clone not usable: %+v", q)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, ".env")); !strings.Contains(string(b), "DATABASE_URL=") {
		t.Fatalf(".env: %s", b)
	}
	st := envelope(t, s.run(wt, "", "", "--json", "status"))
	if list, _ := st["result"].([]any); len(list) != 1 {
		t.Fatalf("status: %v", st)
	}
	r = s.run(root, fmt.Sprintf(`{"hook_event_name":"WorktreeRemove","worktree_path":%q}`, wt), "", "claude-hook", "remove")
	if r.code != 0 || r.stdout != "" {
		t.Fatalf("remove: %+v", r)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("worktree dir still present")
	}
	if b := s.run(root, "", "git", "branch", "--list", "worktree-agent-1"); strings.TrimSpace(b.stdout) != "" {
		t.Fatal("merged branch created by the hook should be deleted")
	}
	st = envelope(t, s.run(root, "", "", "--json", "status", "--all"))
	if list, _ := st["result"].([]any); len(list) != 0 {
		t.Fatalf("registry not empty after remove: %v", st)
	}
}
