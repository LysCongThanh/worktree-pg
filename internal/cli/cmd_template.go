package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// TemplateAPI is the engine surface behind `template status|refresh|gc`
// (milestone M6). The CLI discovers it with a type assertion so this lane does
// not depend on when the engine lane lands it; until then the subcommands
// report NOT_IMPLEMENTED (exit 1). Results are rendered as JSON (`--json`
// envelope .result, or indented JSON for humans).
type TemplateAPI interface {
	// TemplateStatus lists the templates of the app at path: name, fingerprint,
	// current/stale, built_at, last_used_at, clones using it.
	TemplateStatus(ctx context.Context, path string) (any, error)
	// TemplateRefresh builds the template for the current inputs (force: rebuild
	// even when the fingerprint is unchanged). Existing clones are untouched.
	TemplateRefresh(ctx context.Context, path string, force bool) (any, error)
	// TemplateGC keeps [template].keep templates and drops the rest plus orphan
	// _build databases; dry run unless apply (invariant S13).
	TemplateGC(ctx context.Context, path string, apply bool) (any, error)
}

// cmdTemplate dispatches `template status | refresh [--force] | gc [--yes]`.
func cmdTemplate(a *app, ctx context.Context, args []string) int {
	if len(args) == 0 {
		return a.finish("template", nil, nil, usageErr("template needs a subcommand: status | refresh | gc"), nil)
	}
	sub, rest := args[0], args[1:]
	name := "template " + sub
	fs := a.flagSet(name, "template status | refresh [--force] | gc [--yes]")
	var force, yes *bool
	switch sub {
	case "status":
	case "refresh":
		force = fs.Bool("force", false, "rebuild even if the fingerprint is unchanged")
	case "gc":
		yes = fs.Bool("yes", false, "apply: drop what the dry run lists")
	default:
		return a.finish(name, nil, nil, usageErr("unknown template subcommand %q", sub), nil)
	}
	if code, stop := a.parse(fs, name, rest); stop {
		return code
	}
	if code, stop := a.noArgs(fs, name); stop {
		return code
	}
	e, path, err := a.engine()
	if err != nil {
		return a.finish(name, nil, nil, err, nil)
	}
	api, ok := any(e).(TemplateAPI)
	if !ok {
		err := errs.New(errs.NotImplemented, errs.CodeNotImplemented, "%s is not implemented yet", name).
			WithHint("`worktree-pg up` builds the template on demand")
		return a.finish(name, nil, nil, err, nil)
	}
	var res any
	switch sub {
	case "status":
		res, err = api.TemplateStatus(ctx, path)
	case "refresh":
		res, err = api.TemplateRefresh(ctx, path, *force)
	case "gc":
		res, err = api.TemplateGC(ctx, path, *yes)
	}
	return a.finish(name, res, nil, err, func(w io.Writer) { printJSON(w, res) })
}

// printJSON renders a result as indented JSON for humans.
func printJSON(w io.Writer, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintln(w, v)
		return
	}
	fmt.Fprintln(w, string(b))
}
