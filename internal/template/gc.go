package template

import (
	"context"
	"sort"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
)

// GC actions reported per candidate.
const (
	ActionWouldDrop = "would_drop"
	ActionDropped   = "dropped"
	ActionSkipped   = "skipped"
)

// DropLockTimeout bounds DROP DATABASE of an old template: an in-flight clone
// holds a lock on it and must win (spike S3).
const DropLockTimeout = "5s"

// Candidate is one template or orphan build database considered by GC.
type Candidate struct {
	Kind   string `json:"kind"` // template | build
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Action string `json:"action"`
}

// GCOptions selects what GC may remove.
type GCOptions struct {
	App     string
	Keep    int                  // templates to keep ([template].keep)
	Current string               // template of the current fingerprint: always kept
	Recency map[string]time.Time // template name -> last used (registry); unknown = oldest
	Apply   bool                 // false = dry run (invariant S13)
}

// GC drops the app's marked templates beyond the Keep most recent (never the
// current one) and orphan build databases whose advisory lock is free. Existing
// clones are untouched: clones do not depend on their template (spike S3).
func GC(ctx context.Context, a *pg.Admin, o GCOptions) ([]Candidate, error) {
	dbs, err := a.MarkedDatabases(ctx)
	if err != nil {
		return nil, err
	}
	var tpls []string
	var out []Candidate
	for _, d := range dbs { // names are recomputed from the marker so other apps never match
		m, ok := names.ParseMarker(d.Comment)
		if !ok || m.FP == "" {
			continue
		}
		switch {
		case m.Kind == names.KindTemplate && d.Name == names.Template(o.App, m.FP):
			tpls = append(tpls, d.Name)
		case m.Kind == names.KindTemplateBuild && d.Name == names.TemplateBuild(o.App, m.FP):
			out = append(out, dropOne(ctx, a, Candidate{Kind: "build", Name: d.Name, Reason: "orphan template build"},
				names.Template(o.App, m.FP), true, o.Apply))
		}
	}
	sort.SliceStable(tpls, func(i, j int) bool {
		if (tpls[i] == o.Current) != (tpls[j] == o.Current) {
			return tpls[i] == o.Current
		}
		ti, tj := o.Recency[tpls[i]], o.Recency[tpls[j]]
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return tpls[i] > tpls[j]
	})
	for i, t := range tpls {
		if i < o.Keep || t == o.Current {
			continue
		}
		out = append(out, dropOne(ctx, a, Candidate{Kind: "template", Name: t, Reason: "beyond [template].keep"}, t, false, o.Apply))
	}
	return out, nil
}

// dropOne removes one candidate under its template's advisory lock (skipped when
// a build holds it). Templates lose IS_TEMPLATE first and get it back when the
// DROP fails, e.g. on lock_timeout during an in-flight clone (spike D3).
func dropOne(ctx context.Context, a *pg.Admin, c Candidate, lockName string, isBuild, apply bool) Candidate {
	if !apply {
		c.Action = ActionWouldDrop
		return c
	}
	skip := func(why string) Candidate { c.Action, c.Reason = ActionSkipped, c.Reason+"; "+why; return c }
	key := pg.LockKey(lockName)
	locked, err := a.TryLock(ctx, key)
	if err != nil {
		return skip(err.Error())
	}
	if !locked {
		return skip("a template build holds its lock")
	}
	defer a.Unlock(context.WithoutCancel(ctx), key) //nolint:errcheck // session end also unlocks
	if isBuild {
		if err := a.DropDatabase(ctx, c.Name, true); err != nil {
			return skip(err.Error())
		}
		c.Action = ActionDropped
		return c
	}
	_ = a.Exec(ctx, "SET lock_timeout = '"+DropLockTimeout+"'")
	defer a.Exec(context.WithoutCancel(ctx), "RESET lock_timeout") //nolint:errcheck // session-local
	if err := a.SetTemplate(ctx, c.Name, false); err != nil {
		return skip(err.Error())
	}
	if err := a.DropDatabase(ctx, c.Name, false); err != nil {
		_ = a.SetTemplate(context.WithoutCancel(ctx), c.Name, true)
		return skip(err.Error())
	}
	c.Action = ActionDropped
	return c
}
