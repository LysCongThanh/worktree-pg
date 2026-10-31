package template

import (
	"context"
	"io"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
)

// lockPoll is how often a waiting process re-tries the template advisory lock.
const lockPoll = 250 * time.Millisecond

// Builder ensures the published template for one fingerprint exists (spec §5
// "Template build"). The admin connection holds the advisory lock while building.
type Builder struct {
	Admin    *pg.Admin
	Cfg      *config.Config
	Dir      string // where user commands run (worktree toplevel)
	FP       string // template fingerprint
	Host     string // host written into DSN vars for user commands
	Owner    string // owner role (per_worktree) — "" in shared mode
	OwnerPwd string // HMAC password of Owner (secret)
	StateDir string // logs go to <StateDir>/logs
	LogKey   string // log file prefix (worktree key)
	Wait     time.Duration
	Echo     io.Writer // live command output in verbose mode (nil = log only)
	Getenv   func(string) string
	Force    bool // rebuild even when the template exists (`template refresh --force`)
}

// Name is the published template database name.
func (b *Builder) Name() string { return names.Template(b.Cfg.Name, b.FP) }

// Ensure returns the template name, building it when missing. built reports
// whether this call built it. Concurrent callers wait on the advisory lock; at
// most one builds (spec §8 concurrency). Timeout: Wait, else [template].timeout.
func (b *Builder) Ensure(ctx context.Context) (name string, built bool, err error) {
	name = b.Name()
	if ok, err := b.published(ctx); !b.Force && (ok || err != nil) {
		return name, false, err
	}
	key := pg.LockKey(name)
	wait := b.Wait
	if wait <= 0 {
		wait = b.Cfg.Template.Timeout.Duration
	}
	deadline := time.Now().Add(wait)
	for {
		locked, err := b.Admin.TryLock(ctx, key)
		if err != nil {
			return name, false, err
		}
		if locked {
			break
		}
		if ok, err := b.published(ctx); !b.Force && (ok || err != nil) { // another process finished
			return name, false, err
		}
		if time.Now().After(deadline) {
			return name, false, errs.New(errs.Busy, errs.CodeTemplateTimeout, "timed out after %s waiting for template %s", wait, name).
				WithHint("another worktree-pg process is building it; retry with --wait")
		}
		select {
		case <-ctx.Done():
			return name, false, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
	defer b.Admin.Unlock(context.WithoutCancel(ctx), key) //nolint:errcheck // session end also unlocks
	ok, err := b.published(ctx)
	if err != nil || (ok && !b.Force) {
		return name, false, err
	}
	if ok { // forced refresh: drop the current template first (clones are untouched)
		if c := dropOne(ctx, b.Admin, Candidate{Kind: "template", Name: name}, name, false, true); c.Action != ActionDropped {
			return name, false, errs.New(errs.Busy, errs.CodeLockTimeout, "cannot replace template %s: %s", name, c.Reason).
				WithHint("a clone is in progress; retry shortly")
		}
	}
	return name, true, b.build(ctx, name)
}

// published reports whether the template exists and is ours. A foreign database
// with that name is refused; ours left non-template by an interrupted gc (spike
// D3) is restored to IS_TEMPLATE true.
func (b *Builder) published(ctx context.Context) (bool, error) {
	d, err := b.Admin.Database(ctx, b.Name())
	if err != nil || d == nil {
		return false, err
	}
	m, ok := names.ParseMarker(d.Comment)
	if !ok || m.Kind != names.KindTemplate {
		return false, foreign("database", d.Name)
	}
	if !d.IsTemplate {
		if err := b.Admin.SetTemplate(ctx, d.Name, true); err != nil {
			return false, pg.Wrap(err, "restore IS_TEMPLATE on %s", d.Name)
		}
	}
	return true, nil
}

// build runs under the advisory lock: drop a stale marked build DB, create the
// build DB from template1 (owned by the admin), hand its public schema to the
// owner role, create extensions, run prepare/migrate/seed as the owner, then
// freeze and publish by rename (invariant S3). Any failure drops the build DB.
func (b *Builder) build(ctx context.Context, name string) error {
	build := names.TemplateBuild(b.Cfg.Name, b.FP)
	if err := b.dropStaleBuild(ctx, build); err != nil {
		return err
	}
	if err := b.Admin.CreateDatabase(ctx, build, "template1", "", ""); err != nil {
		return pg.Wrap(err, "create template build database %s", build)
	}
	err := b.Admin.CommentDatabase(ctx, build, names.Marker{Kind: names.KindTemplateBuild, FP: b.FP}.String())
	if err == nil {
		err = b.prepareBuild(ctx, build)
	}
	if err == nil {
		err = b.populate(ctx, build)
	}
	if err == nil {
		err = b.publish(ctx, build, name)
	}
	if err != nil {
		_ = b.Admin.DropDatabase(context.WithoutCancel(ctx), build, true) // ours: created above
		return err
	}
	return nil
}

// dropStaleBuild removes a leftover build DB from a crashed run. Holding the
// advisory lock proves no live builder owns it; an unmarked one is foreign.
func (b *Builder) dropStaleBuild(ctx context.Context, build string) error {
	d, err := b.Admin.Database(ctx, build)
	if err != nil || d == nil {
		return err
	}
	if m, ok := names.ParseMarker(d.Comment); !ok || m.Kind != names.KindTemplateBuild {
		return foreign("database", build)
	}
	return pg.Wrap(b.Admin.DropDatabase(ctx, build, true), "drop stale build %s", build)
}

// publish freezes the build DB and publishes it under its final name: no
// connections, sessions terminated (build DB only, S2), IS_TEMPLATE, template
// marker, then the atomic rename.
func (b *Builder) publish(ctx context.Context, build, name string) error {
	steps := []func() error{
		func() error { return b.Admin.SetAllowConnections(ctx, build, false) },
		func() error { return b.Admin.TerminateSessions(ctx, build) },
		func() error { return b.Admin.SetTemplate(ctx, build, true) },
		func() error {
			return b.Admin.CommentDatabase(ctx, build, names.Marker{Kind: names.KindTemplate, FP: b.FP}.String())
		},
		func() error { return b.Admin.RenameDatabase(ctx, build, name) },
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return pg.Wrap(err, "publish template %s", name)
		}
	}
	return nil
}

// foreign is the FOREIGN_RESOURCE refusal for an unmarked object we would touch.
func foreign(kind, name string) error {
	return errs.New(errs.Conflict, errs.CodeForeignResource, "%s %s exists but was not created by worktree-pg", kind, name).
		WithHint("rename or drop it yourself, or change `name` in %s", config.FileName)
}
