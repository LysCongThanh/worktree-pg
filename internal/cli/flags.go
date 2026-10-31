package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/LysCongThanh/worktree-pg/internal/engine"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// globals are the flags accepted before the command and by every command:
// `worktree-pg --json up` and `worktree-pg up --json` are equivalent.
type globals struct {
	JSON    bool
	Path    string
	Config  string
	Quiet   bool
	Verbose bool
}

// app is one CLI invocation: parsed globals, streams and injectable seams.
type app struct {
	g      globals
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
	open   func(engine.Options) (*engine.Engine, error)
}

// newApp wires an invocation to real streams and the real engine.
func newApp(stdin io.Reader, stdout, stderr io.Writer) *app {
	return &app{stdin: stdin, stdout: stdout, stderr: stderr, getenv: os.Getenv, open: engine.Open}
}

// flagSet returns a FlagSet for command name with the global flags bound. Each
// bound default is the value parsed so far, so flags given before the command
// survive the second parse.
func (a *app) flagSet(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.BoolVar(&a.g.JSON, "json", a.g.JSON, "print exactly one JSON document on stdout")
	fs.StringVar(&a.g.Path, "path", a.g.Path, "worktree directory (default: current directory)")
	fs.StringVar(&a.g.Config, "config", a.g.Config, "config file (default: <toplevel>/.worktree-pg.toml)")
	fs.BoolVar(&a.g.Quiet, "q", a.g.Quiet, "quiet: errors only")
	fs.BoolVar(&a.g.Verbose, "v", a.g.Verbose, "verbose: echo user command output")
	fs.Usage = func() {
		fmt.Fprintf(a.stderr, "usage: worktree-pg %s\n\nflags:\n", synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parse parses args into fs. stop is true when the command must end now: help
// was requested (exit 0) or the flags are invalid (usage error, exit 3).
func (a *app) parse(fs *flag.FlagSet, cmd string, args []string) (code int, stop bool) {
	err := fs.Parse(args)
	if err == nil {
		return 0, false
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0, true
	}
	return a.finish(cmd, nil, nil, usageErr("%v", err), nil), true
}

// noArgs rejects positional arguments for commands that take none.
func (a *app) noArgs(fs *flag.FlagSet, cmd string) (int, bool) {
	if fs.NArg() == 0 {
		return 0, false
	}
	return a.finish(cmd, nil, nil, usageErr("unexpected arguments: %s", strings.Join(fs.Args(), " ")), nil), true
}

// path returns the absolute worktree path (--path or the working directory).
// Existence is not required: `down` must work for a deleted directory.
func (a *app) path() (string, error) {
	p := a.g.Path
	if p == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", errs.Wrap(errs.Internal, errs.CodeInternal, err, "getwd")
		}
		p = wd
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", errs.Wrap(errs.Config, CodeUsage, err, "--path")
	}
	return abs, nil
}

// engine opens the engine rooted at the worktree path.
func (a *app) engine() (*engine.Engine, string, error) {
	path, err := a.path()
	if err != nil {
		return nil, "", err
	}
	e, err := a.open(engine.Options{
		Cwd: path, ConfigPath: a.g.Config, Getenv: a.getenv, Stderr: a.stderr, Verbose: a.g.Verbose,
	})
	return e, path, err
}

// usageErr is an invalid invocation (exit 3).
func usageErr(format string, args ...any) error {
	return errs.New(errs.Config, CodeUsage, format, args...).WithHint("run `worktree-pg help`")
}
