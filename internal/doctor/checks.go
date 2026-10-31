// Package doctor checks the prerequisites of worktree-pg: git, the repository and
// its config, the state directory and secret key, docker (managed clusters), the
// admin connection and its privileges, extensions and installed hooks.
//
// Checks only read: doctor never creates databases, roles, files or containers.
package doctor

import (
	"context"
	"os"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// Check statuses.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
	Skip = "skip"
)

// CodeDoctorFailed is the error code when at least one check fails (exit 4).
const CodeDoctorFailed = "DOCTOR_FAILED"

// Check is one line of the doctor report.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Report is `doctor --json` .result.
type Report struct {
	Checks []Check `json:"checks"`
}

// Options select what doctor inspects.
type Options struct {
	Cwd        string              // directory to start from
	ConfigPath string              // explicit config ("" = <toplevel>/.worktree-pg.toml)
	StateDir   string              // resolved state directory
	Home       string              // home directory (user-scope Claude settings)
	Getenv     func(string) string // nil = os.Getenv
	Timeout    time.Duration       // per network check (0 = 5s)
}

// run accumulates checks and shares what earlier checks discovered.
type run struct {
	o      Options
	rep    Report
	root   string         // repository toplevel ("" if not in a repo)
	cfg    *config.Config // loaded config (nil if missing/invalid)
	cfgErr error
}

// Run executes every check in order and never returns early: the report is the
// full picture even when the first check fails.
func Run(ctx context.Context, o Options) Report {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	r := &run{o: o, rep: Report{Checks: []Check{}}}
	r.checkGit(ctx)
	r.checkRepoAndConfig(ctx)
	r.checkState()
	r.checkDocker(ctx)
	r.checkAdmin(ctx)
	r.checkHooks(ctx)
	return r.rep
}

// Err returns DOCTOR_FAILED (exit 4) when any check failed, else nil.
func (rep Report) Err() error {
	n := 0
	for _, c := range rep.Checks {
		if c.Status == Fail {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return errs.New(errs.Prereq, CodeDoctorFailed, "%d doctor check(s) failed", n)
}

// add appends one check.
func (r *run) add(name, status, detail, hint string) {
	r.rep.Checks = append(r.rep.Checks, Check{Name: name, Status: status, Detail: detail, Hint: hint})
}

// hintOf returns the remediation hint carried by a worktree-pg error, if any.
func hintOf(err error) string {
	if e := errs.As(err); e != nil {
		return e.Hint
	}
	return ""
}
