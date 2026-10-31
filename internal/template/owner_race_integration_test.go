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

// Two builds of different fingerprints create the owner role at the same time:
// the loser blocks on the winner's uncommitted CREATE ROLE and then gets 23505
// (not 42710). EnsureOwner must treat that as "already exists", re-check the
// marker (committed together with the role) and succeed.
func TestEnsureOwnerConcurrentCreate(t *testing.T) {
	for i, kind := range []string{"superuser", "nosuper"} {
		t.Run(kind, func(t *testing.T) {
			app := pgtest.App(t)
			adm := pgtest.Admins(t, app)[i]
			ctx := context.Background()
			owner := names.OwnerRole(app)
			winner := pgtest.Connect(t, adm.DSN)
			loser := pgtest.Connect(t, adm.DSN)
			tx, err := winner.Conn().Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			marker := names.Marker{Kind: names.KindOwner}.String()
			if _, err := tx.Exec(ctx, "CREATE ROLE "+pg.Ident(owner)+" LOGIN; COMMENT ON ROLE "+pg.Ident(owner)+" IS "+pg.Literal(marker)); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := EnsureOwner(ctx, loser, owner, "pw"); done <- err }()
			time.Sleep(300 * time.Millisecond) // loser is now blocked on the winner's insert
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("concurrent EnsureOwner: %v", err)
			}
		})
	}
}
