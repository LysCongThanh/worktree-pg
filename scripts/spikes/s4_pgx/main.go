//go:build spike

// Spike S4: observe SQLSTATE 55006 (source database in use) through pgx as *pgconn.PgError,
// measure the server-side ~5s wait, and confirm CREATE/DROP DATABASE run via PgConn().Exec
// (simple protocol, outside a transaction block). Also checks 42P04 on duplicate CREATE.
//
// Usage: WTPG_SPIKE_DSN=postgres://postgres:pw@127.0.0.1:15416/postgres go run -tags spike ./scripts/spikes/s4_pgx
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// execSimple runs a statement with the simple protocol and returns the PgError (if any).
func execSimple(ctx context.Context, c *pgx.Conn, sql string) error {
	_, err := c.PgConn().Exec(ctx, sql).ReadAll()
	return err
}

// code extracts the SQLSTATE of a pgx error, or "" for non-PG errors.
func code(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func main() {
	ctx := context.Background()
	dsn := os.Getenv("WTPG_SPIKE_DSN")
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		fmt.Println("connect:", err)
		os.Exit(1)
	}
	defer admin.Close(ctx)
	for _, q := range []string{
		"DROP DATABASE IF EXISTS s4_clone WITH (FORCE)",
		"DROP DATABASE IF EXISTS s4_src WITH (FORCE)",
		"CREATE DATABASE s4_src",
	} {
		if err := execSimple(ctx, admin, q); err != nil {
			fmt.Println(q, "->", err)
			os.Exit(1)
		}
	}
	fmt.Println("OK CREATE DATABASE via PgConn().Exec (simple protocol)")

	// Hold a session on the source database so CREATE ... TEMPLATE hits 55006.
	cfg, _ := pgx.ParseConfig(dsn)
	cfg.Database = "s4_src"
	holder, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		fmt.Println("holder:", err)
		os.Exit(1)
	}
	start := time.Now()
	err = execSimple(ctx, admin, "CREATE DATABASE s4_clone TEMPLATE s4_src")
	fmt.Printf("busy template: code=%s waited=%s err=%v\n", code(err), time.Since(start).Round(100*time.Millisecond), err)
	holder.Close(ctx)

	// Extended protocol inside implicit tx: pgx Exec with no args uses simple protocol too; show tx refusal explicitly.
	tx, _ := admin.Begin(ctx)
	_, err = tx.Exec(ctx, "CREATE DATABASE s4_intx")
	fmt.Printf("CREATE DATABASE in tx: code=%s\n", code(err))
	_ = tx.Rollback(ctx)

	if err := execSimple(ctx, admin, "CREATE DATABASE s4_clone TEMPLATE s4_src"); err != nil {
		fmt.Println("clone after release:", err)
	} else {
		fmt.Println("OK clone after holder released")
	}
	err = execSimple(ctx, admin, "CREATE DATABASE s4_clone TEMPLATE s4_src")
	fmt.Printf("duplicate: code=%s\n", code(err))
	_ = execSimple(ctx, admin, "DROP DATABASE s4_clone WITH (FORCE)")
	_ = execSimple(ctx, admin, "DROP DATABASE s4_src WITH (FORCE)")
}
