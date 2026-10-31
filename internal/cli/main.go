// Package cli implements the worktree-pg command line (stdlib flag, one FlagSet
// per command). It only talks to internal/engine (plus hooks and doctor), so the
// CLI, the hooks and the future MCP server share one behaviour.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// Version is set at build time: -ldflags "-X github.com/LysCongThanh/worktree-pg/internal/cli.Version=v0.1.0".
var Version = "dev"

const usage = `worktree-pg: one migrated Postgres template, one database + dev ports per git worktree.

Usage: worktree-pg [global flags] <command> [flags]

Commands:
  up        [--wait D] [--reassign-ports] [--dry-run]   provision this worktree (idempotent)
  down      [--keep-env] [--remove-worktree]            tear down database, role and env block
  status    [--all]                                     show provisioned worktrees
  env       [--format dotenv|shell|json] [--override]   print this worktree's variables
  run       [--override] -- cmd [args...]               run a command with those variables
  gc        [--yes]                                     reconcile with 'git worktree list' (dry run unless --yes)
  template  status | refresh [--force] | gc [--yes]     manage template databases
  doctor                                                check prerequisites
  hook      install|uninstall claude [--scope local|project|user] [--print]
  hook      install|uninstall git [--print]
  version                                               print the version

Global flags (before or after the command):
  --json  --path DIR  --config FILE  -q  -v

Exit codes: 0 ok, 1 internal, 2 safety refusal, 3 config/usage, 4 prerequisite,
5 user command failed, 6 busy, 7 not provisioned, 130 interrupted.
`

// command runs one subcommand with its own arguments and returns the exit code.
type command func(a *app, ctx context.Context, args []string) int

// commands is filled in init to avoid an initialisation cycle with cmdHelp.
var commands map[string]command

func init() {
	commands = map[string]command{
		"up": cmdUp, "down": cmdDown, "status": cmdStatus, "env": cmdEnv, "run": cmdRun,
		"gc": cmdGC, "template": cmdTemplate, "doctor": cmdDoctor, "hook": cmdHook,
		"version": cmdVersion, "help": cmdHelp,
		"claude-hook": cmdClaudeHook, "git-hook": cmdGitHook, // internal, called by hooks
	}
}

// Main runs the CLI and returns the process exit code. SIGINT/SIGTERM cancel the
// context; commands report that as exit 130.
func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return newApp(os.Stdin, os.Stdout, os.Stderr).run(ctx, args)
}

// run parses global flags, then dispatches to the command.
func (a *app) run(ctx context.Context, args []string) int {
	fs := a.flagSet("worktree-pg", "[global flags] <command> [flags]")
	fs.Usage = func() { fmt.Fprint(a.stderr, usage) }
	if code, stop := a.parse(fs, "", args); stop {
		return code
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprint(a.stderr, usage)
		return a.finish("", nil, nil, usageErr("missing command"), nil)
	}
	cmd, ok := commands[rest[0]]
	if !ok {
		return a.finish(rest[0], nil, nil, usageErr("unknown command %q", rest[0]), nil)
	}
	return cmd(a, ctx, rest[1:])
}

// cmdVersion prints the version (or {"version": ...} with --json).
func cmdVersion(a *app, _ context.Context, args []string) int {
	fs := a.flagSet("version", "version")
	if code, stop := a.parse(fs, "version", args); stop {
		return code
	}
	v := currentVersion()
	res := map[string]string{"version": v}
	return a.finish("version", res, nil, nil, func(w io.Writer) { fmt.Fprintln(w, "worktree-pg", v) })
}

// cmdHelp prints the usage text on stdout.
func cmdHelp(a *app, _ context.Context, _ []string) int {
	fmt.Fprint(a.stdout, usage)
	return exitOK
}
