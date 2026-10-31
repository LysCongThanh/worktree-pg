package pg

import (
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

func TestURLAndKV(t *testing.T) {
	c := Cred{Host: "127.0.0.1", Port: 5600, User: "shop_wt_a_1", Password: "p@ss/w rd", Database: "shop_wt_a_1", Params: "sslmode=disable&application_name=api"}
	if got := URL(c); got != "postgres://shop_wt_a_1:p%40ss%2Fw%20rd@127.0.0.1:5600/shop_wt_a_1?sslmode=disable&application_name=api" {
		t.Fatalf("URL = %s", got)
	}
	if got := KV(c); got != "host=127.0.0.1 port=5600 user=shop_wt_a_1 password='p@ss/w rd' dbname=shop_wt_a_1 sslmode=disable application_name=api" {
		t.Fatalf("KV = %s", got)
	}
	if got := URL(Cred{Host: "::1", Port: 1, User: "u", Database: "d"}); got != "postgres://u@[::1]:1/d" {
		t.Fatalf("IPv6 URL = %s", got)
	}
	if got := kvQuote(`a'b\c`); got != `'a\'b\\c'` {
		t.Fatalf("kvQuote = %s", got)
	}
}

func TestPGEnvAndDSNVars(t *testing.T) {
	base := Cred{Host: "localhost", Port: 6000, User: "r", Password: "pw", Database: "d", Params: "sslmode=disable"}
	env := PGEnv(base)
	for _, want := range []string{"PGHOST=localhost", "PGPORT=6000", "PGUSER=r", "PGPASSWORD=pw", "PGDATABASE=d", "PGSSLMODE=disable"} {
		if !slices.Contains(env, want) {
			t.Fatalf("PGEnv %v misses %s", env, want)
		}
	}
	list := []config.DSN{
		{Var: "DATABASE_URL", Role: OwnerRef, Format: "url", Params: "sslmode=disable"},
		{Var: "APP_URL", Role: "app_rw", PasswordEnv: "APP_PW", Format: "url", Host: "127.0.0.1"},
		{Var: "RO", Role: "app_ro", Format: "kv"},
	}
	vars := DSNVars(list, base, func(k string) string { return map[string]string{"APP_PW": "apw"}[k] })
	want := []string{"postgres://r:pw@localhost:6000/d?sslmode=disable", "postgres://app_rw:apw@127.0.0.1:6000/d", "host=localhost port=6000 user=app_ro dbname=d"}
	for i, v := range vars {
		if v.Value != want[i] || v.Key != list[i].Var || v.Secret != (i < 2) {
			t.Fatalf("var %d = %+v, want %s", i, v, want[i])
		}
	}
}

func TestQuotingAndKeys(t *testing.T) {
	if Literal(`it's \n`) != `'it''s \n'` || Ident(`we"ird`) != `"we""ird"` {
		t.Fatal("quoting")
	}
	if LockKey("shop_tpl_abcd1234") != LockKey("shop_tpl_abcd1234") || LockKey("a") == LockKey("b") {
		t.Fatal("LockKey not stable/distinct")
	}
}

func TestSQLErrorMapping(t *testing.T) {
	priv := &pgconn.PgError{Code: CodeInsufficientPrivilege, Message: "permission denied"}
	if !IsState(priv, CodeInsufficientPrivilege) || SQLState(errors.New("x")) != "" {
		t.Fatal("IsState/SQLState")
	}
	if err := Wrap(priv, "create"); errs.CodeOf(err) != errs.CodePGPrivilege || errs.ExitCode(err) != 4 {
		t.Fatalf("privilege: %v", err)
	}
	if err := Wrap(&pgconn.PgError{Code: CodeObjectInUse}, "x"); errs.ExitCode(err) != 1 {
		t.Fatalf("other: %v", err)
	}
	typed := errs.New(errs.Conflict, errs.CodeOwnerGuard, "g")
	if Wrap(typed, "x") != error(typed) || Wrap(nil, "x") != nil {
		t.Fatal("typed errors must pass through")
	}
}
