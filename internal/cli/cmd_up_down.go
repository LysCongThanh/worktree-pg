package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/LysCongThanh/worktree-pg/internal/engine"
	"github.com/LysCongThanh/worktree-pg/internal/envfile"
	"github.com/LysCongThanh/worktree-pg/internal/hooks"
)

// cmdUp provisions the worktree (idempotent).
func cmdUp(a *app, ctx context.Context, args []string) int {
	fs := a.flagSet("up", "up [--wait D] [--reassign-ports] [--dry-run]")
	wait := fs.Duration("wait", 0, "wait up to D for a concurrent template build or lease (0 = fail fast)")
	reassign := fs.Bool("reassign-ports", false, "allocate new ports instead of keeping sticky ones")
	dry := fs.Bool("dry-run", false, "print the plan without changing anything")
	if code, stop := a.parse(fs, "up", args); stop {
		return code
	}
	if code, stop := a.noArgs(fs, "up"); stop {
		return code
	}
	e, path, err := a.engine()
	var res *engine.UpResult
	if err == nil {
		res, err = e.Up(ctx, engine.UpRequest{Path: path, Wait: *wait, ReassignPorts: *reassign, DryRun: *dry, Origin: "cli"})
	}
	var warns []engine.Warning
	if res != nil {
		warns = res.Warnings
	}
	return a.finish("up", res, warns, err, func(w io.Writer) { printUp(w, res) })
}

// cmdDown tears the worktree down; --remove-worktree also removes the git
// worktree (checked BEFORE anything is dropped, so a refusal leaves all intact).
func cmdDown(a *app, ctx context.Context, args []string) int {
	fs := a.flagSet("down", "down [--keep-env] [--remove-worktree]")
	keepEnv := fs.Bool("keep-env", false, "leave the managed env blocks in place")
	removeWT := fs.Bool("remove-worktree", false, "also `git worktree remove` this (linked, unlocked) worktree")
	if code, stop := a.parse(fs, "down", args); stop {
		return code
	}
	if code, stop := a.noArgs(fs, "down"); stop {
		return code
	}
	e, path, err := a.engine()
	var rm *hooks.Removal
	branch := "" // branch created by the Claude hook (read before Down deletes the entry)
	if err == nil && *removeWT {
		if rm, err = hooks.CheckRemovable(ctx, path); err == nil {
			if entry, _ := hooks.FindEntry(e.Store, path); entry != nil {
				branch = entry.CreatedBranch
			}
		}
	}
	var res *engine.DownResult
	if err == nil {
		res, err = e.Down(ctx, engine.DownRequest{Path: path, KeepEnv: *keepEnv})
	}
	if err == nil && rm != nil {
		err = a.removeWorktree(ctx, rm, branch)
	}
	var warns []engine.Warning
	if res != nil {
		warns = res.Warnings
	}
	return a.finish("down", res, warns, err, func(w io.Writer) { printDown(w, res, rm != nil) })
}

// removeWorktree runs `git worktree remove` honouring [hooks.claude].remove_force
// and deletes the branch the Claude hook created (merged only).
func (a *app) removeWorktree(ctx context.Context, rm *hooks.Removal, branch string) error {
	cfg, _, err := hooks.LoadConfig(rm.Worktree.Path, a.g.Config)
	if err != nil {
		return err
	}
	return rm.Remove(ctx, cfg.Hooks.Claude.RemoveForce, branch, a.stderr)
}

// printUp renders the human summary of `up`.
func printUp(w io.Writer, r *engine.UpResult) {
	if r == nil {
		return
	}
	state := "ready"
	if len(r.Actions) == 0 {
		state = "ready (no changes)"
	}
	fmt.Fprintf(w, "%s %s\n", r.Worktree.Path, state)
	fmt.Fprintf(w, "  database  %s (role %s) on %s:%d, PostgreSQL %d\n", r.Database, r.Role, r.Cluster.Host, r.Cluster.Port, r.Cluster.PGMajor)
	tpl := r.Template.Name
	if r.Template.Built {
		tpl += " (built now)"
	}
	if r.Template.Stale {
		tpl += " (stale: inputs changed, run `template refresh`)"
	}
	fmt.Fprintf(w, "  template  %s\n", tpl)
	if len(r.Ports) > 0 {
		fmt.Fprintf(w, "  ports     %s\n", formatPorts(r.Ports))
	}
	printEnvFiles(w, r.EnvFiles)
	if len(r.Actions) > 0 {
		fmt.Fprintf(w, "  actions   %s\n", strings.Join(r.Actions, ", "))
	}
}

// printDown renders the human summary of `down`.
func printDown(w io.Writer, r *engine.DownResult, removed bool) {
	if r == nil {
		return
	}
	fmt.Fprintf(w, "%s torn down\n", r.Worktree.Path)
	if r.Database != "" {
		fmt.Fprintf(w, "  dropped   database %s, role %s\n", r.Database, r.Role)
	}
	printEnvFiles(w, r.EnvFiles)
	if removed {
		fmt.Fprintln(w, "  worktree  removed")
	}
	for _, o := range r.Orphans {
		fmt.Fprintf(w, "  orphan    %s (could not be dropped)\n", o)
	}
}

// printEnvFiles lists env files and what happened to them.
func printEnvFiles(w io.Writer, files []envfile.Result) {
	for _, f := range files {
		what := "unchanged"
		switch {
		case f.Created:
			what = "created"
		case f.Changed:
			what = "updated"
		}
		fmt.Fprintf(w, "  env       %s (%s)\n", f.Path, what)
		if len(f.DuplicateKeys) > 0 {
			fmt.Fprintf(w, "            also set outside the block: %s\n", strings.Join(f.DuplicateKeys, ", "))
		}
	}
}

// formatPorts renders ports sorted by variable: "ADMIN_PORT=5601 PORT=5600".
func formatPorts(p map[string]int) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, p[k])
	}
	return strings.Join(parts, " ")
}
