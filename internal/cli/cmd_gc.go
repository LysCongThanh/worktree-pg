package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/LysCongThanh/worktree-pg/internal/engine"
)

// cmdGC reconciles the registry with `git worktree list`. It is a dry run unless
// --yes is given (invariant S13).
func cmdGC(a *app, ctx context.Context, args []string) int {
	fs := a.flagSet("gc", "gc [--yes]")
	yes := fs.Bool("yes", false, "apply: drop what the dry run lists")
	if code, stop := a.parse(fs, "gc", args); stop {
		return code
	}
	if code, stop := a.noArgs(fs, "gc"); stop {
		return code
	}
	e, path, err := a.engine()
	var rep *engine.GCReport
	if err == nil {
		rep, err = e.GC(ctx, engine.GCRequest{Path: path, Apply: *yes})
	}
	var warns []engine.Warning
	if rep != nil {
		warns = rep.Warnings
	}
	return a.finish("gc", rep, warns, err, func(w io.Writer) { printGC(w, rep) })
}

// printGC lists candidates (dry run) or actions taken.
func printGC(w io.Writer, r *engine.GCReport) {
	if r == nil {
		return
	}
	if len(r.Items) == 0 {
		fmt.Fprintln(w, "nothing to collect")
		return
	}
	for _, it := range r.Items {
		where := it.Path
		if where == "" {
			where = it.Key
		}
		fmt.Fprintf(w, "%-10s %-9s %s %s (%s)\n", it.Action, it.Kind, it.Name, where, it.Reason)
	}
	if r.DryRun {
		fmt.Fprintln(w, "dry run: re-run with --yes to apply")
	}
}
