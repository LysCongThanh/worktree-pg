package template

import (
	"context"
	"strings"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/runner"
)

// EnsureOwner creates (or verifies) the owner role that owns every template
// object, with the deterministic password, and makes the admin a member of it.
// A same-named role without our owner marker is refused (invariant S1).
func EnsureOwner(ctx context.Context, a *pg.Admin, owner, password string) (created bool, err error) {
	r, err := a.Role(ctx, owner)
	if err != nil {
		return false, err
	}
	if r == nil {
		err = a.CreateMarkedRole(ctx, owner, password, names.Marker{Kind: names.KindOwner}.String())
		// lost a race with a parallel build of another fingerprint: 42710 if it had
		// committed before we looked, 23505 if we blocked on its in-flight insert
		if pg.IsState(err, pg.CodeDuplicateObject) || pg.IsState(err, pg.CodeUniqueViolation) {
			return EnsureOwner(ctx, a, owner, password)
		}
		if err != nil {
			return false, pg.Wrap(err, "create owner role %s", owner)
		}
		created = true
	} else if m, ok := names.ParseMarker(r.Comment); !ok || m.Kind != names.KindOwner {
		return false, foreign("role", owner)
	} else if err := a.SetPassword(ctx, owner, password); err != nil {
		return false, pg.Wrap(err, "set owner role password")
	}
	return created, pg.Wrap(a.GrantToSelf(ctx, owner), "grant %s to the admin", owner)
}

// prepareBuild runs the admin-only steps inside the build DB: hand the public
// schema to the owner (per_worktree mode) and create [template].extensions.
func (b *Builder) prepareBuild(ctx context.Context, build string) error {
	if b.Owner != "" {
		if _, err := EnsureOwner(ctx, b.Admin, b.Owner, b.OwnerPwd); err != nil {
			return err
		}
	}
	c, err := b.Admin.OnDatabase(ctx, build)
	if err != nil {
		return err
	}
	defer c.Close(context.WithoutCancel(ctx))
	var stmts []string
	if b.Owner != "" {
		o := pg.Ident(b.Owner)
		stmts = append(stmts, "ALTER SCHEMA public OWNER TO "+o,
			"GRANT CREATE, TEMPORARY ON DATABASE "+pg.Ident(build)+" TO "+o)
	}
	for _, e := range b.Cfg.Template.Extensions {
		stmts = append(stmts, "CREATE EXTENSION IF NOT EXISTS "+pg.Ident(e))
	}
	for _, s := range stmts {
		if err := c.Exec(ctx, s); err != nil {
			return pg.Wrap(err, "prepare template build (%s)", firstWords(s))
		}
	}
	return nil
}

// populate runs prepare_cmd, migrate_cmd and seed_cmd in order against the build
// DB, connected as the owner role (or the admin in shared mode). Credentials only
// travel in the environment; the admin DSN variable is stripped (invariant S6).
func (b *Builder) populate(ctx context.Context, build string) error {
	cred := b.Admin.Cred(build)
	if b.Owner != "" {
		cred.User, cred.Password = b.Owner, b.OwnerPwd
	}
	cred.Host = b.Host
	env := CommandEnv(b.Cfg.DSN, cred, b.Getenv)
	t := b.Cfg.Template
	cmds := []struct{ label, code, script string }{
		{"prepare_cmd", errs.CodePrepareFailed, t.PrepareCmd},
		{"migrate_cmd", errs.CodeMigrateFailed, t.MigrateCmd},
		{"seed_cmd", errs.CodeSeedFailed, t.SeedCmd},
	}
	for _, c := range cmds {
		if strings.TrimSpace(c.script) == "" {
			continue
		}
		err := runner.Run(ctx, runner.Cmd{
			Label: c.label, Code: c.code, Script: c.script, Dir: b.Dir, Env: env,
			Strip:   []string{b.Cfg.Cluster.AdminDSNEnv},
			LogPath: runner.LogPath(b.StateDir, b.LogKey, c.label, time.Now()),
			Echo:    b.Echo, Timeout: t.Timeout.Duration,
		})
		if err != nil {
			return err
		}
	}
	_ = runner.PruneLogs(b.StateDir, runner.KeepLogs)
	return nil
}

// CommandEnv is the environment of a user command connected as cred: PG* libpq
// variables plus every [[dsn]] variable pointed at cred.Database.
func CommandEnv(dsns []config.DSN, cred pg.Cred, getenv func(string) string) []string {
	env := pg.PGEnv(cred)
	for _, v := range pg.DSNVars(dsns, cred, getenv) {
		env = append(env, v.Key+"="+v.Value)
	}
	return env
}

// firstWords shortens a DDL statement for error messages (never holds secrets).
func firstWords(s string) string {
	f := strings.Fields(s)
	if len(f) > 3 {
		f = f[:3]
	}
	return strings.Join(f, " ")
}
