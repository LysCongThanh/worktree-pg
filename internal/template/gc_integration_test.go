//go:build integration

package template

import (
	"context"
	"testing"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
)

// buildN publishes n templates of app with different fingerprints (oldest first).
func buildN(t *testing.T, a *pg.Admin, app string, n int) []string {
	t.Helper()
	var out []string
	for i := 0; i < n; i++ {
		b := newBuilder(t, a, app, migrateSQL+"-- v"+string(rune('a'+i))+"\n")
		name, _, err := b.Ensure(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return out
}

func recency(tpls []string) map[string]time.Time {
	m := map[string]time.Time{}
	for i, t := range tpls {
		m[t] = time.Unix(int64(1000+i), 0)
	}
	return m
}

// TestInvariant_S13_TemplateGCDryRunThenApply: dry run drops nothing; apply keeps
// the current template plus the most recent ones up to keep.
func TestInvariant_S13_TemplateGCDryRunThenApply(t *testing.T) {
	app := pgtest.App(t)
	a := pgtest.Connect(t, pgtest.Admins(t, app)[1].DSN)
	tpls := buildN(t, a, app, 3)
	ctx := context.Background()
	o := GCOptions{App: app, Keep: 1, Current: tpls[0], Recency: recency(tpls)}
	dry, err := GC(ctx, a, o)
	if err != nil || len(dry) != 2 {
		t.Fatalf("dry run: %v %+v", err, dry)
	}
	for _, c := range dry {
		if c.Action != ActionWouldDrop {
			t.Fatalf("dry run acted: %+v", c)
		}
	}
	o.Apply = true
	got, err := GC(ctx, a, o)
	if err != nil || len(got) != 2 {
		t.Fatalf("apply: %v %+v", err, got)
	}
	for _, n := range tpls[1:] {
		if d, _ := a.Database(ctx, n); d != nil {
			t.Fatalf("%s survived gc", n)
		}
	}
	if d, _ := a.Database(ctx, tpls[0]); d == nil {
		t.Fatal("current template dropped")
	}
}

// TestTemplateGCRestoresIsTemplate (spike D3): a DROP that times out on a lock
// held by another session leaves the template IS_TEMPLATE true.
func TestTemplateGCRestoresIsTemplate(t *testing.T) {
	app := pgtest.App(t)
	a := pgtest.Connect(t, pgtest.Admins(t, app)[1].DSN)
	tpls := buildN(t, a, app, 2)
	ctx := context.Background()
	super := pgtest.Connect(t, pgtest.SuperDSN(t))
	tx, err := super.Conn().Begin(ctx) // COMMENT locks the database object until commit
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	marker := names.Marker{Kind: names.KindTemplate}.String()
	if _, err := tx.Exec(ctx, "COMMENT ON DATABASE "+pg.Ident(tpls[0])+" IS "+pg.Literal(marker)); err != nil {
		t.Fatal(err)
	}
	got, err := GC(ctx, a, GCOptions{App: app, Keep: 1, Current: tpls[1], Recency: recency(tpls), Apply: true})
	if err != nil || len(got) != 1 || got[0].Action != ActionSkipped {
		t.Fatalf("want skipped, got %v %+v", err, got)
	}
	d, err := a.Database(ctx, tpls[0])
	if err != nil || d == nil || !d.IsTemplate {
		t.Fatalf("template not restored: %+v %v", d, err)
	}
}

// TestRefreshForceRebuilds: Force replaces the template under the same name.
func TestRefreshForceRebuilds(t *testing.T) {
	app := pgtest.App(t)
	a := pgtest.Connect(t, pgtest.Admins(t, app)[1].DSN)
	b := newBuilder(t, a, app, migrateSQL)
	ctx := context.Background()
	if _, _, err := b.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	b.Force = true
	name, built, err := b.Ensure(ctx)
	if err != nil || !built {
		t.Fatalf("forced refresh: built=%v err=%v", built, err)
	}
	assertPublished(t, a, name, b.FP)
}

// TestOrphanBuildIsCollected: a marked build DB with a free lock is dropped.
func TestOrphanBuildIsCollected(t *testing.T) {
	app := pgtest.App(t)
	a := pgtest.Connect(t, pgtest.Admins(t, app)[1].DSN)
	ctx := context.Background()
	fp := "ab12cd34ef"
	build := names.TemplateBuild(app, fp)
	if err := a.CreateDatabase(ctx, build, "template1", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.CommentDatabase(ctx, build, names.Marker{Kind: names.KindTemplateBuild, FP: fp}.String()); err != nil {
		t.Fatal(err)
	}
	got, err := GC(ctx, a, GCOptions{App: app, Keep: 1, Apply: true})
	if err != nil || len(got) != 1 || got[0].Action != ActionDropped {
		t.Fatalf("orphan build: %v %+v", err, got)
	}
}
