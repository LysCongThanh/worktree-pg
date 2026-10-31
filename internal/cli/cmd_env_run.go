package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/LysCongThanh/worktree-pg/internal/envfile"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/runner"
)

// execFn replaces the process image (`run`); tests swap it out.
var execFn = syscall.Exec

// envResult is `env --format json` .result. It is the only JSON document that
// carries passwords (spec §6).
type envResult struct {
	Path string        `json:"path"`
	Vars []envfile.Var `json:"vars"`
}

// cmdEnv prints the provisioned variables. It reads the registry only and never
// provisions (NOT_PROVISIONED, exit 7).
func cmdEnv(a *app, ctx context.Context, args []string) int {
	fs := a.flagSet("env", "env [--format dotenv|shell|json] [--override]")
	format := fs.String("format", "dotenv", "dotenv | shell | json")
	override := fs.Bool("override", false, "shell: also export keys already set in the environment")
	if code, stop := a.parse(fs, "env", args); stop {
		return code
	}
	if code, stop := a.noArgs(fs, "env"); stop {
		return code
	}
	if *format == "json" {
		a.g.JSON = true
	}
	if *format != "dotenv" && *format != "shell" && *format != "json" {
		return a.finish("env", nil, nil, usageErr("--format must be dotenv, shell or json"), nil)
	}
	vars, path, err := a.loadVars(ctx)
	if err != nil || a.g.JSON {
		return a.finish("env", envResult{Path: path, Vars: vars}, nil, err, nil)
	}
	if *format == "dotenv" {
		writeLines(a.stdout, envfile.RenderDotenv(vars))
		return exitOK
	}
	keep, skipped := splitInherited(vars, *override, os.LookupEnv)
	for _, v := range skipped {
		fmt.Fprintf(a.stderr, "worktree-pg: skipped %s (already set in the environment; use --override)\n", v.Key)
	}
	writeLines(a.stdout, envfile.RenderShell(keep))
	return exitOK
}

// cmdRun replaces worktree-pg with `cmd args...` (syscall.Exec) running with the
// worktree's variables. Inherited variables win unless --override.
func cmdRun(a *app, ctx context.Context, args []string) int {
	fs := a.flagSet("run", "run [--override] -- cmd [args...]")
	override := fs.Bool("override", false, "worktree variables replace inherited ones")
	if code, stop := a.parse(fs, "run", args); stop {
		return code
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return a.finish("run", nil, nil, usageErr("missing command: worktree-pg run -- cmd [args...]"), nil)
	}
	vars, _, err := a.loadVars(ctx)
	if err != nil {
		return a.finish("run", nil, nil, err, nil)
	}
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return a.finish("run", nil, nil, errs.Wrap(errs.UserCmd, "COMMAND_NOT_FOUND", err, "cannot run %s", argv[0]), nil)
	}
	err = execFn(bin, argv, RunEnv(os.Environ(), vars, *override))
	return a.finish("run", nil, nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "exec %s", bin), nil)
}

// splitInherited separates vars already set in the inherited environment
// (skipped) from the rest (keep). With override nothing is skipped.
func splitInherited(vars []envfile.Var, override bool, lookup func(string) (string, bool)) (keep, skipped []envfile.Var) {
	for _, v := range vars {
		if _, set := lookup(v.Key); set && !override {
			skipped = append(skipped, v)
			continue
		}
		keep = append(keep, v)
	}
	return keep, skipped
}

// RunEnv merges worktree vars into base. Without override, a variable already in
// base is kept (same rule as `env --format shell`); with override vars win.
func RunEnv(base []string, vars []envfile.Var, override bool) []string {
	have := map[string]bool{}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		have[k] = true
	}
	var extra []string
	for _, v := range vars {
		if override || !have[v.Key] {
			extra = append(extra, v.Key+"="+v.Value)
		}
	}
	return runner.MergeEnv(base, extra, nil)
}

// loadVars opens the engine and reads the worktree's variables.
func (a *app) loadVars(ctx context.Context) ([]envfile.Var, string, error) {
	e, path, err := a.engine()
	if err != nil {
		return nil, path, err
	}
	vars, err := e.Env(ctx, path)
	if vars == nil {
		vars = []envfile.Var{}
	}
	return vars, path, err
}

// writeLines prints one line per element.
func writeLines(w io.Writer, lines []string) {
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}
