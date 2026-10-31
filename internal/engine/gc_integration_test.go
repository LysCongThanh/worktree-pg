//go:build integration

package engine

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
)

// itemFor finds the gc item for a path or name.
func itemFor(rep *GCReport, kind, pathOrName string) *GCItem {
	for i, it := range rep.Items {
		if it.Kind == kind && (it.Path == pathOrName || it.Name == pathOrName) {
			return &rep.Items[i]
		}
	}
	return nil
}

// TestInvariant_S13_GCDryRunDefaultNeverLocked: removed and prunable worktrees
// are reported, dropped only with Apply; a locked worktree is never touched;
// unrecorded marked clones are dropped only when their path is gone and they
// were created on this host (another host's paths cannot be checked from here).
func TestInvariant_S13_GCDryRunDefaultNeverLocked(t *testing.T) {
	app := pgtest.App(t)
	f := newRepo(t, app, adminDSNs(t, app)["nosuper"], "")
	ctx := context.Background()
	a, b, c := f.worktree("gone-a"), f.worktree("gone-b"), f.worktree("locked-c")
	ra, rb, rc := f.up(a), f.up(b), f.up(c)
	f.git(f.main, "worktree", "lock", c)
	for _, p := range []string{a, b, c} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
	f.git(f.main, "worktree", "prune") // a disappears from the list; b... is pruned too
	f.git(f.main, "worktree", "add", "-q", b, "gone-b")
	if err := os.RemoveAll(b); err != nil { // b listed again, now prunable
		t.Fatal(err)
	}
	super := pgtest.Connect(t, pgtest.SuperDSN(t))
	tool := pgtest.Connect(t, f.env["WTPG_IT_ADMIN"]) // orphans were created by the tool's admin
	orphan, live, remote := app+"_wt_orphan_deadbeef", app+"_wt_live_cafebabe", app+"_wt_remote_feedface"
	hosts := map[string]string{orphan: localHost(), live: localHost(), remote: "teammate-laptop"}
	for db, path := range map[string]string{orphan: "/nonexistent/worktree", live: f.main, remote: "/nonexistent/worktree"} {
		if err := tool.CreateDatabase(ctx, db, "template0", "", ""); err != nil {
			t.Fatal(err)
		}
		m := names.Marker{Kind: names.KindClone, Key: names.Key(db), Path: path, Host: hosts[db]}
		if err := tool.CommentDatabase(ctx, db, m.String()); err != nil {
			t.Fatal(err)
		}
	}
	dry, err := f.e.GC(ctx, GCRequest{Path: f.main})
	if err != nil || !dry.DryRun {
		t.Fatal(err)
	}
	for _, want := range []struct{ kind, key, action string }{
		{"worktree", a, gcWouldDrop}, {"worktree", b, gcWouldDrop}, {"worktree", c, gcSkipped},
		{"database", orphan, gcWouldDrop}, {"database", live, gcSkipped}, {"database", remote, gcSkipped},
	} {
		if it := itemFor(dry, want.kind, want.key); it == nil || it.Action != want.action {
			t.Fatalf("dry run item %s %s: %+v (all %+v)", want.kind, want.key, it, dry.Items)
		}
	}
	if d, _ := super.Database(ctx, ra.Database); d == nil {
		t.Fatal("dry run dropped a database")
	}
	app2, err := f.e.GC(ctx, GCRequest{Path: f.main, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, db := range []string{ra.Database, rb.Database, orphan} {
		if d, _ := super.Database(ctx, db); d != nil {
			t.Fatalf("%s survived gc --yes (%+v)", db, app2.Items)
		}
	}
	for _, db := range []string{rc.Database, live, remote} {
		if d, _ := super.Database(ctx, db); d == nil {
			t.Fatalf("%s must survive gc (locked / live path / other host)", db)
		}
	}
	f.git(f.main, "worktree", "unlock", c)
}

// TestTemplateStaleAndGC: changed inputs mark the clone stale without rebuilding
// it; refresh builds the new template; template gc keeps only the current one.
func TestTemplateStaleAndGC(t *testing.T) {
	app := pgtest.App(t)
	f := newRepo(t, app, adminDSNs(t, app)["nosuper"], "keep = 1")
	ctx := context.Background()
	wt := f.worktree("stale")
	first := f.up(wt)
	if err := os.WriteFile(filepath.Join(wt, "db", "m.sql"), []byte("create table other(id int);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sts, err := f.e.Status(ctx, StatusRequest{Path: wt})
	if err != nil || !sts[0].Template.Stale {
		t.Fatalf("status should report template_stale: %+v %v", sts, err)
	}
	again := f.up(wt)
	if len(again.Actions) != 0 || !again.Template.Stale || again.Template.Name != first.Template.Name {
		t.Fatalf("up on stale clone: %+v", again)
	}
	ref, err := f.e.TemplateRefresh(ctx, wt, false)
	if err != nil || !ref.(*TemplateRefreshResult).Template.Built {
		t.Fatalf("refresh: %+v %v", ref, err)
	}
	if ref, _ := f.e.TemplateRefresh(ctx, wt, false); ref.(*TemplateRefreshResult).Template.Built {
		t.Fatal("second refresh rebuilt")
	}
	if ref, err := f.e.TemplateRefresh(ctx, wt, true); err != nil || !ref.(*TemplateRefreshResult).Template.Built {
		t.Fatalf("forced refresh: %v", err)
	}
	list, err := f.e.TemplateStatus(ctx, wt)
	if infos := list.([]TemplateInfo); err != nil || len(infos) != 2 {
		t.Fatalf("template status %+v %v", list, err)
	}
	g, err := f.e.TemplateGC(ctx, wt, true)
	rep := g.(*GCReport)
	if it := itemFor(rep, "template", first.Template.Name); err != nil || it == nil || it.Action != gcDropped {
		t.Fatalf("template gc %+v %v", rep, err)
	}
	if c := pgtest.Connect(t, f.env["WTPG_IT_ADMIN"]); true {
		if d, _ := c.Database(ctx, first.Database); d == nil {
			t.Fatal("template gc touched an existing clone")
		}
	}
}

// TestConcurrentUpsBuildOnce: parallel ups of distinct worktrees on a cold
// template build it exactly once and get unique ports.
func TestConcurrentUpsBuildOnce(t *testing.T) {
	app := pgtest.App(t)
	f := newRepo(t, app, adminDSNs(t, app)["nosuper"], "")
	const n = 6
	var paths []string
	for i := 0; i < n; i++ {
		paths = append(paths, f.worktree("par-"+string(rune('a'+i))))
	}
	results := make([]*UpResult, n)
	errc := make([]error, n)
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errc[i] = f.e.Up(context.Background(), UpRequest{Path: p, Wait: 0})
		}()
	}
	wg.Wait()
	builds, seen := 0, map[int]bool{}
	for i, r := range results {
		if errc[i] != nil {
			t.Fatalf("up %d: %v", i, errc[i])
		}
		if slices.Contains(r.Actions, "build_template") {
			builds++
		}
		for _, p := range r.Ports {
			if seen[p] {
				t.Fatalf("port %d assigned twice", p)
			}
			seen[p] = true
		}
	}
	if builds != 1 {
		t.Fatalf("%d template builds, want 1", builds)
	}
}
