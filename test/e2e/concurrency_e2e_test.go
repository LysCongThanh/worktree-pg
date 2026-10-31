//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// parallel runs fn(i) for i in [0,n) concurrently and returns the results.
func parallel(n int, fn func(i int) result) []result {
	out := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); out[i] = fn(i) }(i)
	}
	wg.Wait()
	return out
}

// TestE2E_ParallelUpColdTemplate: 16 separate processes run `up --wait` on 16
// fresh worktrees while no template exists yet. Exactly one process builds the
// template (one migrate run), every worktree gets its own DB and port.
func TestE2E_ParallelUpColdTemplate(t *testing.T) {
	const n = 16
	app := newApp(t)
	for _, a := range admins(t, app) {
		t.Run(a.name, func(t *testing.T) {
			sub := app + a.name[:1]
			t.Cleanup(func() { dropApp(t, sub) })
			s, buildLog := newPGSandbox(t, a.dsn)
			root := s.repo(sampleRepo(sub, migrateAll))
			wts := make([]string, n)
			for i := range wts {
				wts[i] = filepath.Join(s.dir, fmt.Sprintf("wt%02d", i))
				if r := s.run(root, "", "git", "worktree", "add", "-q", "-b", fmt.Sprintf("b%02d", i), wts[i]); r.code != 0 {
					t.Fatalf("git worktree add: %+v", r)
				}
			}
			res := parallel(n, func(i int) result { return s.run(wts[i], "", "", "--json", "up", "--wait", "5m") })
			ports, built := map[float64]bool{}, 0
			for i, r := range res {
				if r.code != 0 {
					t.Fatalf("up %d: %+v", i, r)
				}
				m := envelope(t, r)["result"].(map[string]any)
				if m["template"].(map[string]any)["built"] == true {
					built++
				}
				p := m["ports"].(map[string]any)["PORT"].(float64)
				if ports[p] {
					t.Fatalf("duplicate port %v", p)
				}
				ports[p] = true
			}
			if b := builds(t, buildLog); b != 1 || built != 1 {
				t.Fatalf("template builds: migrate runs %d, built flags %d (want 1/1)", b, built)
			}
			if got := wtNames(t, "db", sub); len(got) != n {
				t.Fatalf("want %d clones, got %v", n, got)
			}
			if got := names(t, "db", sub+`\_tpl\_%`); len(got) != 1 {
				t.Fatalf("want 1 template and no _build leftovers, got %v", got)
			}
		})
	}
}

// TestE2E_ParallelUpSameWorktree: 4 concurrent `up` of ONE worktree. Without
// --wait, one wins and the others either see it ready (0) or the held lease
// (exit 6 LEASE_HELD); with --wait all succeed. Exactly one DB either way.
func TestE2E_ParallelUpSameWorktree(t *testing.T) {
	app := newApp(t)
	s, _ := newPGSandbox(t, superDSN(t))
	root := s.repo(sampleRepo(app, migrateAll))
	for _, wait := range []string{"0s", "2m"} {
		wt := filepath.Join(s.dir, "same-"+wait)
		if r := s.run(root, "", "git", "worktree", "add", "-q", "-b", "same-"+wait, wt); r.code != 0 {
			t.Fatalf("git worktree add: %+v", r)
		}
		res := parallel(4, func(int) result { return s.run(wt, "", "", "--json", "up", "--wait", wait) })
		ok := 0
		for _, r := range res {
			switch {
			case r.code == 0:
				ok++
			case r.code == 6 && wait == "0s":
				if e := envelope(t, r)["error"].(map[string]any); e["code"] != "LEASE_HELD" && e["code"] != "TEMPLATE_BUILD_TIMEOUT" {
					t.Fatalf("exit 6 with code %v", e["code"])
				}
			default:
				t.Fatalf("wait=%s: %+v", wait, r)
			}
		}
		if ok == 0 || (wait != "0s" && ok != 4) {
			t.Fatalf("wait=%s: %d of 4 succeeded", wait, ok)
		}
	}
	if got := wtNames(t, "db", app); len(got) != 2 {
		t.Fatalf("want 2 clones (one per worktree), got %v", got)
	}
}
