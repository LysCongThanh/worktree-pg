//go:build integration

package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
)

func TestConnectProbesCapabilities(t *testing.T) {
	app := pgtest.App(t)
	admins := pgtest.Admins(t, app)
	su, ns := pgtest.Connect(t, admins[0].DSN), pgtest.Connect(t, admins[1].DSN)
	if !su.Info.Superuser || su.Info.Major < 15 || su.Info.VersionNum < 150000 {
		t.Fatalf("superuser info %+v", su.Info)
	}
	if ns.Info.Superuser || !ns.Info.CreateDB || !ns.Info.CreateRole || ns.Info.User != app+"_adm_nosuper" {
		t.Fatalf("nosuper info %+v", ns.Info)
	}
	if ns.Info.NeedsSetGrant() != (ns.Info.Major >= 16) || su.Info.NeedsSetGrant() {
		t.Fatal("NeedsSetGrant")
	}
}

// TestCloneRetries55006: a session held on the source for longer than the
// server's own 5 s wait makes the first CREATE fail with 55006; the retry wins.
func TestCloneRetries55006(t *testing.T) {
	app := pgtest.App(t)
	a := pgtest.Connect(t, pgtest.Admins(t, app)[1].DSN)
	ctx := context.Background()
	src := app + "_src"
	if err := a.CreateDatabase(ctx, src, "template1", "", ""); err != nil {
		t.Fatal(err)
	}
	holder, err := a.OnDatabase(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(6 * time.Second); holder.Close(context.Background()) }()
	start := time.Now()
	if err := a.CloneDatabase(ctx, app+"_dst", src, "", ""); err != nil {
		t.Fatalf("clone with retry: %v", err)
	}
	if time.Since(start) < 5*time.Second {
		t.Fatal("clone did not wait for the held session")
	}
	err = a.CreateDatabase(ctx, app+"_dst", src, "", "")
	if !pg.IsState(err, pg.CodeDuplicateDatabase) {
		t.Fatalf("duplicate: want 42P04, got %v", err)
	}
}

func TestCloneMissingTemplate(t *testing.T) {
	app := pgtest.App(t)
	a := pgtest.Connect(t, pgtest.SuperDSN(t))
	err := a.CloneDatabase(context.Background(), app+"_x", app+"_nope", "", "")
	if err != pg.ErrTemplateGone {
		t.Fatalf("want ErrTemplateGone, got %v", err)
	}
}

func TestAdvisoryLockExclusive(t *testing.T) {
	dsn := pgtest.SuperDSN(t)
	a, b := pgtest.Connect(t, dsn), pgtest.Connect(t, dsn)
	ctx := context.Background()
	key := pg.LockKey("pgtest-lock-" + pgtest.App(t))
	if ok, err := a.TryLock(ctx, key); !ok || err != nil {
		t.Fatal("first lock", err)
	}
	if ok, _ := b.TryLock(ctx, key); ok {
		t.Fatal("second session got the lock")
	}
	if err := a.Unlock(ctx, key); err != nil {
		t.Fatal(err)
	}
	if ok, _ := b.TryLock(ctx, key); !ok {
		t.Fatal("lock not released")
	}
}
