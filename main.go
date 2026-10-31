// Command worktree-pg gives every git worktree its own Postgres database (cloned
// from a migrated template), its own dev ports and wired env files.
package main

import (
	"os"

	"github.com/LysCongThanh/worktree-pg/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
