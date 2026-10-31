//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2E_MigrateFailureLeavesNoOrphan (S4): a broken migration fails `up` with
// exit 5 + MIGRATE_FAILED + a log path, and leaves no database (no clone, no
// template, no _build) and no worktree role behind — for both the CLI and the
// Claude hook, which also rolls back its worktree and branch. Fixing the
// migration and re-running then succeeds.
func TestE2E_MigrateFailureLeavesNoOrphan(t *testing.T) {
	app := newApp(t)
	for _, a := range admins(t, app) {
		t.Run(a.name, func(t *testing.T) {
			sub := app + a.name[:1]
			t.Cleanup(func() { dropApp(t, sub) })
			s, _ := newPGSandbox(t, a.dsn)
			files := sampleRepo(sub, migrateAll)
			files["db/migrations/003_broken.sql"] = "ALTER TABLE no_such_table ADD COLUMN x int;\n"
			root := s.repo(files)
			wt := filepath.Join(s.dir, "broken")
			s.run(root, "", "git", "worktree", "add", "-q", "-b", "broken", wt)

			r := s.run(wt, "", "", "--json", "up")
			if r.code != 5 {
				t.Fatalf("want exit 5, got %+v", r)
			}
			e := envelope(t, r)["error"].(map[string]any)
			if e["code"] != "MIGRATE_FAILED" || !strings.HasSuffix(fmt.Sprint(e["log"]), ".log") {
				t.Fatalf("error envelope: %v", e)
			}
			assertNoObjects(t, sub)

			r = s.run(root, `{"hook_event_name":"WorktreeCreate","name":"agent-x"}`, "", "claude-hook", "create")
			if r.code == 0 || r.stdout != "" {
				t.Fatalf("claude create should fail with empty stdout: %+v", r)
			}
			if _, err := os.Stat(filepath.Join(root, ".claude", "worktrees", "agent-x")); !os.IsNotExist(err) {
				t.Fatal("claude worktree not rolled back")
			}
			assertNoObjects(t, sub)

			// Fix the migration in the worktree: the next up builds and clones.
			if err := os.Remove(filepath.Join(wt, "db/migrations/003_broken.sql")); err != nil {
				t.Fatal(err)
			}
			if r := s.run(wt, "", "", "--json", "up"); r.code != 0 {
				t.Fatalf("up after fix: %+v", r)
			}
			if r := s.run(wt, "", "", "down"); r.code != 0 {
				t.Fatalf("down: %+v", r)
			}
			if got := wtNames(t, "db", sub); len(got) != 0 {
				t.Fatalf("down left %v", got)
			}
		})
	}
}

// assertNoObjects: no database at all and no worktree role exist for app (the
// shared template owner role is cluster infrastructure and may remain).
func assertNoObjects(t *testing.T, app string) {
	t.Helper()
	if got := names(t, "db", app+`\_%`); len(got) != 0 {
		t.Fatalf("orphan databases: %v", got)
	}
	if got := wtNames(t, "role", app); len(got) != 0 {
		t.Fatalf("orphan roles: %v", got)
	}
}

// TestE2E_ForeignObjectsRefused: a database that matches our naming pattern but
// carries no marker is refused (exit 2 FOREIGN_RESOURCE), never adopted or
// dropped, and gc --yes leaves it alone (S1).
func TestE2E_ForeignObjectsRefused(t *testing.T) {
	app := newApp(t)
	s, _ := newPGSandbox(t, superDSN(t))
	root := s.repo(sampleRepo(app, migrateAll))
	wt := filepath.Join(s.dir, "victim")
	s.run(root, "", "git", "worktree", "add", "-q", "-b", "victim", wt)
	plan := envelope(t, s.run(wt, "", "", "--json", "up", "--dry-run"))
	db, _ := plan["result"].(map[string]any)["database"].(string)
	if db == "" {
		t.Fatalf("dry run did not report the database name: %v", plan)
	}
	sqlExec(t, "CREATE DATABASE "+db)
	r := s.run(wt, "", "", "--json", "up")
	if r.code != 2 || envelope(t, r)["error"].(map[string]any)["code"] != "FOREIGN_RESOURCE" {
		t.Fatalf("want FOREIGN_RESOURCE exit 2, got %+v", r)
	}
	s.run(wt, "", "", "down")
	s.run(root, "", "", "gc", "--yes")
	if got := names(t, "db", db); len(got) != 1 {
		t.Fatalf("foreign database %s was dropped", db)
	}
}
