package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/LysCongThanh/worktree-pg/internal/doctor"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// cmdDoctor runs read-only prerequisite checks. Any failed check → DOCTOR_FAILED
// (exit 4); the --json envelope keeps the full report in .result even then.
func cmdDoctor(a *app, ctx context.Context, args []string) int {
	fs := a.flagSet("doctor", "doctor")
	if code, stop := a.parse(fs, "doctor", args); stop {
		return code
	}
	if code, stop := a.noArgs(fs, "doctor"); stop {
		return code
	}
	path, err := a.path()
	if err != nil {
		return a.finish("doctor", nil, nil, err, nil)
	}
	state, err := registry.StateDir(a.getenv)
	if err != nil {
		return a.finish("doctor", nil, nil, err, nil)
	}
	home, _ := os.UserHomeDir()
	rep := doctor.Run(ctx, doctor.Options{Cwd: path, ConfigPath: a.g.Config, StateDir: state, Home: home, Getenv: a.getenv})
	return a.finishKeep("doctor", rep, rep.Err(), func(w io.Writer) { printDoctor(w, rep) })
}

// printDoctor renders one line per check, with hints under warnings/failures.
func printDoctor(w io.Writer, rep doctor.Report) {
	marks := map[string]string{doctor.OK: "ok  ", doctor.Warn: "WARN", doctor.Fail: "FAIL", doctor.Skip: "skip"}
	for _, c := range rep.Checks {
		fmt.Fprintf(w, "[%s] %-18s %s\n", marks[c.Status], c.Name, c.Detail)
		if c.Hint != "" && c.Status != doctor.OK {
			fmt.Fprintf(w, "       %-18s hint: %s\n", "", c.Hint)
		}
	}
}
