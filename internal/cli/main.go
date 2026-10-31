// Package cli implements the worktree-pg command line (stdlib flag, one FlagSet
// per command). Milestone M3 fills in the commands; this file provides the entry
// point, `version` and the usage text so the binary builds from day one.
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// Version is set at build time: -ldflags "-X github.com/LysCongThanh/worktree-pg/internal/cli.Version=v0.1.0".
var Version = "dev"

const usage = `worktree-pg: one migrated Postgres template, one database + dev ports per git worktree.

Usage: worktree-pg <command> [flags]

Commands:
  up        provision this worktree (idempotent)
  down      tear down this worktree's database, role and env block
  status    show provisioned worktrees
  env       print this worktree's variables
  run       run a command with this worktree's variables
  gc        reconcile with 'git worktree list' (dry run unless --yes)
  template  status | refresh | gc
  doctor    check prerequisites
  hook      install | uninstall claude|git
  version   print the version
`

// Main runs the CLI and returns the process exit code.
func Main(args []string) int { return run(args, os.Stdout, os.Stderr) }

// run is Main with injectable streams for tests.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stderr, usage)
		if len(args) == 0 {
			return 3
		}
		return 0
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, "worktree-pg", Version)
		return 0
	case "up", "down", "status", "env", "run", "gc", "template", "doctor", "hook", "claude-hook", "git-hook":
		err := errs.New(errs.NotImplemented, errs.CodeNotImplemented, "%s is not implemented yet", args[0])
		fmt.Fprintln(stderr, "worktree-pg:", err)
		return errs.ExitCode(err)
	default:
		fmt.Fprintf(stderr, "worktree-pg: unknown command %q\n\n%s", args[0], usage)
		return 3
	}
}
