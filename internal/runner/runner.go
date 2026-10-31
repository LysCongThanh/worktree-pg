// Package runner executes user-configured shell commands (prepare, migrate, seed,
// cluster_setup, post_clone) with secrets passed only through the environment
// (never argv, invariant S6) and output captured to a private log file.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// KeepLogs is how many log files PruneLogs keeps.
const KeepLogs = 20

// Cmd is one user command invocation.
type Cmd struct {
	Label   string        // human name, e.g. "migrate_cmd"
	Code    string        // stable error code on failure, e.g. errs.CodeMigrateFailed
	Script  string        // shell text from config, run via `sh -c`
	Dir     string        // working directory (worktree root)
	Env     []string      // extra KEY=VALUE pairs (may hold secrets; never logged)
	Strip   []string      // inherited variable names to remove (e.g. the admin DSN env)
	LogPath string        // 0600 file receiving stdout+stderr ("" = none)
	Echo    io.Writer     // optional live copy of the output (stderr in verbose mode)
	Timeout time.Duration // 0 = no timeout beyond ctx
}

// Run executes c. A non-zero exit becomes errs.UserCmd with c.Code and the log
// path (exit 5); cancellation kills the whole process group and reports Interrupted.
func Run(ctx context.Context, c Cmd) error {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", c.Script)
	cmd.Dir = c.Dir
	cmd.Env = MergeEnv(os.Environ(), c.Env, c.Strip)
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	var sinks []io.Writer
	if c.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(c.LogPath), 0o700); err != nil {
			return errs.Wrap(errs.Internal, errs.CodeInternal, err, "create log dir")
		}
		f, err := os.OpenFile(c.LogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return errs.Wrap(errs.Internal, errs.CodeInternal, err, "open log")
		}
		defer f.Close()
		fmt.Fprintf(f, "### %s %s: %s\n", time.Now().Format(time.RFC3339), c.Label, c.Script)
		sinks = append(sinks, f)
	}
	if c.Echo != nil {
		sinks = append(sinks, c.Echo)
	}
	out := io.MultiWriter(append(sinks, io.Discard)...)
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errs.New(errs.UserCmd, c.Code, "%s timed out after %s", c.Label, c.Timeout).WithLog(c.LogPath)
		}
		return errs.Wrap(errs.Interrupted, errs.CodeInterrupted, ctx.Err(), "%s interrupted", c.Label)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return errs.New(errs.UserCmd, c.Code, "%s failed with exit code %d", c.Label, ee.ExitCode()).
			WithLog(c.LogPath).WithHint("see the log for the command output")
	}
	return errs.Wrap(errs.UserCmd, c.Code, err, "%s could not start", c.Label).WithLog(c.LogPath)
}

// MergeEnv returns base without the names in strip and without names overridden
// by extra, followed by extra. Later duplicates in extra win.
func MergeEnv(base, extra, strip []string) []string {
	drop := map[string]bool{}
	for _, n := range strip {
		drop[n] = true
	}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		drop[k] = true
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}

// LogPath returns <stateDir>/logs/<key>-<UTC timestamp>-<label>.log.
func LogPath(stateDir, key, label string, now time.Time) string {
	return filepath.Join(stateDir, "logs", fmt.Sprintf("%s-%s-%s.log", key, now.UTC().Format("20060102T150405.000"), label))
}

// PruneLogs keeps the newest keep *.log files in <stateDir>/logs (by name, which
// sorts by timestamp within a key; mtime breaks ties across keys).
func PruneLogs(stateDir string, keep int) error {
	files, err := filepath.Glob(filepath.Join(stateDir, "logs", "*.log"))
	if err != nil || len(files) <= keep {
		return err
	}
	mt := map[string]time.Time{}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			mt[f] = st.ModTime()
		}
	}
	sort.Slice(files, func(i, j int) bool { return mt[files[i]].After(mt[files[j]]) })
	for _, f := range files[keep:] {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
