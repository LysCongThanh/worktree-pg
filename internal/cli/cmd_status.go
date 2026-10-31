package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/LysCongThanh/worktree-pg/internal/engine"
)

// cmdStatus lists the current worktree, or every registered one with --all.
func cmdStatus(a *app, ctx context.Context, args []string) int {
	fs := a.flagSet("status", "status [--all]")
	all := fs.Bool("all", false, "every registered worktree on this machine")
	if code, stop := a.parse(fs, "status", args); stop {
		return code
	}
	if code, stop := a.noArgs(fs, "status"); stop {
		return code
	}
	e, path, err := a.engine()
	var list []engine.WorktreeStatus
	if err == nil {
		list, err = e.Status(ctx, engine.StatusRequest{Path: path, All: *all})
	}
	if list == nil {
		list = []engine.WorktreeStatus{} // JSON: [] rather than null
	}
	return a.finish("status", list, nil, err, func(w io.Writer) { printStatus(w, list) })
}

// printStatus renders a table: path, status, database, ports, notes.
func printStatus(w io.Writer, list []engine.WorktreeStatus) {
	if len(list) == 0 {
		fmt.Fprintln(w, "no provisioned worktrees (run `worktree-pg up`)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tSTATUS\tDATABASE\tCLUSTER\tPORTS\tNOTES")
	for _, s := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s:%d\t%s\t%s\n", s.Worktree.Path, s.Status, s.Database,
			s.Cluster.Host, s.Cluster.Port, formatPorts(s.Ports), statusNotes(s))
	}
	_ = tw.Flush()
}

// statusNotes summarises problems worth a second look.
func statusNotes(s engine.WorktreeStatus) string {
	notes := ""
	add := func(n string) {
		if notes != "" {
			notes += "; "
		}
		notes += n
	}
	if !s.PathExists {
		add("directory missing (gc)")
	}
	if s.Template.Stale {
		add("template stale")
	}
	if h := s.Health; h != nil {
		if !h.DBExists {
			add("database missing")
		}
		if h.DBExists && !h.MarkerOK {
			add("marker mismatch")
		}
		if h.Sessions > 0 {
			add(fmt.Sprintf("%d session(s)", h.Sessions))
		}
	}
	if s.LastError != "" {
		add("last error: " + s.LastError)
	}
	return notes
}
