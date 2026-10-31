//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newPGSandbox is a sandbox whose admin DSN is dsn and whose migrate runs are
// counted in buildLog (one line per real template build).
func newPGSandbox(t *testing.T, dsn string) (s *sandbox, buildLog string) {
	t.Helper()
	s = newSandbox(t, false)
	buildLog = filepath.Join(s.dir, "builds.log")
	s.env = append(s.env, "WORKTREE_PG_ADMIN_DSN="+dsn, "WTPG_E2E_BUILD_LOG="+buildLog)
	return s, buildLog
}

// builds counts real template builds recorded by migrateAll.
func builds(t *testing.T, log string) int {
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return 0
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "build\n")
}

// statusAll decodes `status --all --json` into worktree path -> status entry.
func statusAll(t *testing.T, s *sandbox, dir string) map[string]map[string]any {
	t.Helper()
	env := envelope(t, s.run(dir, "", "", "--json", "status", "--all"))
	out := map[string]map[string]any{}
	list, _ := env["result"].([]any)
	for _, it := range list {
		m := it.(map[string]any)
		out[m["worktree"].(map[string]any)["path"].(string)] = m
	}
	return out
}

// TestE2E_FullScenario follows the plan's verification script against a real
// server, as superuser and as a NOSUPERUSER CREATEDB CREATEROLE admin:
// 3x `git worktree add` (post-checkout hook) + 1 claude-hook create → 4 marked
// DBs/roles, unique sticky ports, 0600 env blocks, idempotent re-run; then gc
// after rm -rf, down, claude-hook remove — never touching a foreign "dev" DB or
// its live session.
func TestE2E_FullScenario(t *testing.T) {
	app := newApp(t)
	for _, a := range admins(t, app) {
		t.Run(a.name, func(t *testing.T) { fullScenario(t, app+a.name[:1], a.dsn) })
	}
}

func fullScenario(t *testing.T, app, dsn string) {
	t.Cleanup(func() { dropApp(t, app) })
	// A developer's own database with a live session, plus a lookalike foreign
	// DB/role with our naming pattern but no marker: none may ever be touched.
	sqlExec(t, "CREATE DATABASE "+app+"_dev")
	alive := heldSession(t, app+"_dev")
	sqlExec(t, "CREATE DATABASE "+app+"_wt_foreign_0000beef")
	sqlExec(t, "CREATE ROLE "+app+"_wt_intruder")

	s, buildLog := newPGSandbox(t, dsn)
	root := s.repo(sampleRepo(app, migrateAll))
	if r := s.run(root, "", "", "hook", "install", "git"); r.code != 0 {
		t.Fatalf("hook install git: %+v", r)
	}
	var wts []string
	for i := 1; i <= 3; i++ {
		wt := filepath.Join(s.dir, fmt.Sprintf("shop-feat%d", i))
		if r := s.run(root, "", "git", "worktree", "add", "-q", "-b", fmt.Sprintf("feat%d", i), wt); r.code != 0 {
			t.Fatalf("git worktree add: %+v", r)
		}
		wts = append(wts, wt)
	}
	r := s.run(root, `{"session_id":"e2e","cwd":"`+root+`","hook_event_name":"WorktreeCreate","name":"agent-1"}`, "", "claude-hook", "create")
	if r.code != 0 {
		t.Fatalf("claude-hook create: %+v", r)
	}
	claudeWT := lastLine(r.stdout)
	wts = append(wts, claudeWT)

	// 4 ready worktrees, 4 marked DBs + roles, one build, unique ports.
	st := statusAll(t, s, root)
	ports := map[float64]string{}
	for _, wt := range wts {
		e, ok := st[wt]
		if !ok || e["status"] != "ready" {
			t.Fatalf("worktree %s not ready: %v", wt, e)
		}
		p := e["ports"].(map[string]any)["PORT"].(float64)
		if prev, dup := ports[p]; dup {
			t.Fatalf("port %v shared by %s and %s", p, prev, wt)
		}
		ports[p] = wt
		assertEnvFile(t, filepath.Join(wt, ".env"))
	}
	if got := wtNames(t, "db", app); len(got) != 5 { // 4 clones + the foreign lookalike
		t.Fatalf("databases: %v", got)
	}
	if got := wtNames(t, "role", app); len(got) != 5 { // 4 roles + the intruder
		t.Fatalf("roles: %v", got)
	}
	if n := builds(t, buildLog); n != 1 {
		t.Fatalf("template built %d times, want 1", n)
	}

	// Idempotent re-run (S11): same ports, no actions, byte-identical env file.
	for _, wt := range wts {
		before, _ := os.ReadFile(filepath.Join(wt, ".env"))
		res := envelope(t, s.run(wt, "", "", "--json", "up"))["result"].(map[string]any)
		if a := res["actions"].([]any); len(a) != 0 {
			t.Fatalf("re-run of %s not idempotent: %v", wt, a)
		}
		if p := res["ports"].(map[string]any)["PORT"].(float64); ports[p] != wt {
			t.Fatalf("port of %s not sticky: %v", wt, p)
		}
		if after, _ := os.ReadFile(filepath.Join(wt, ".env")); string(after) != string(before) {
			t.Fatalf("env bytes changed on re-run of %s", wt)
		}
	}

	// rm -rf + prune, then gc: dry run first, --yes drops exactly that worktree.
	_ = os.RemoveAll(wts[0]) // fixture dir under t.TempDir, simulating a user's rm -rf
	s.run(root, "", "git", "worktree", "prune")
	if r := s.run(root, "", "", "--json", "gc"); r.code != 0 || !strings.Contains(r.stdout, `"would_drop"`) {
		t.Fatalf("gc dry run: %+v", r)
	}
	if len(wtNames(t, "db", app)) != 5 {
		t.Fatal("gc dry run dropped something")
	}
	if r := s.run(root, "", "", "--json", "gc", "--yes"); r.code != 0 {
		t.Fatalf("gc --yes: %+v", r)
	}
	if got := wtNames(t, "db", app); len(got) != 4 {
		t.Fatalf("after gc: %v", got)
	}

	if r := s.run(wts[1], "", "", "down"); r.code != 0 {
		t.Fatalf("down: %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(wts[1], ".env")); strings.Contains(string(b), "DATABASE_URL") {
		t.Fatal("down left the managed env block")
	}
	r = s.run(root, fmt.Sprintf(`{"hook_event_name":"WorktreeRemove","worktree_path":%q}`, claudeWT), "", "claude-hook", "remove")
	if r.code != 0 {
		t.Fatalf("claude-hook remove: %+v", r)
	}
	if _, err := os.Stat(claudeWT); !os.IsNotExist(err) {
		t.Fatal("claude worktree dir still present")
	}
	if r := s.run(wts[2], "", "", "down"); r.code != 0 {
		t.Fatalf("down: %+v", r)
	}

	// Only our objects are gone; the dev DB session survived everything (S1, S2).
	if got := wtNames(t, "db", app); len(got) != 1 || got[0] != app+"_wt_foreign_0000beef" {
		t.Fatalf("databases left: %v", got)
	}
	if got := wtNames(t, "role", app); len(got) != 1 || got[0] != app+"_wt_intruder" {
		t.Fatalf("roles left: %v", got)
	}
	if !alive() {
		t.Fatal("session on the dev database was terminated (S2)")
	}
	if len(statusAll(t, s, root)) != 0 {
		t.Fatal("registry not empty")
	}
}

// assertEnvFile checks the managed block exists and the file is private (S7).
func assertEnvFile(t *testing.T, path string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode %o, want 0600", path, st.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "DATABASE_URL=") || !strings.Contains(string(b), "PORT=") {
		t.Fatalf("%s missing managed vars:\n%s", path, b)
	}
}
