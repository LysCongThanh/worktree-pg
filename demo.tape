# demo.tape: README GIF storyboard for vhs (https://github.com/charmbracelet/vhs).
#   vhs demo.tape            -> docs/demo.gif
#
# Before recording (off camera):
#   - a Postgres reachable via WORKTREE_PG_ADMIN_DSN (e.g. `make itest`, port 15417)
#   - ~/demo/shop: a git repo with db/migrations/*.sql and the README quickstart config,
#     with `worktree-pg hook install git` already run and no worktrees yet
#   - worktree-pg on PATH; a short prompt (PS1='$ ')
#
# Story (about 30 s):
#   1. The config is tiny.                                 (cat .worktree-pg.toml)
#   2. Three agents get three worktrees at once.           (3x git worktree add in parallel)
#      -> first one builds the template, all three clone it
#   3. Each worktree has its own DB and port in .env.      (grep across the three .env files)
#   4. They really are separate databases.                 (insert in one, count in another)
#   5. Nobody waits for migrations again.                  (fourth worktree: ~1.6 s)
#   6. Status, then clean up.                              (status --all, down)

Output docs/demo.gif
Set Shell "bash"
Set FontSize 18
Set Width 1200
Set Height 640
Set Padding 20
Set TypingSpeed 15ms
Set Theme "Catppuccin Mocha"

Hide
Type "export PS1='$ ' && cd ~/demo/shop && clear" Enter
Show

# 1. Config
Type "cat .worktree-pg.toml" Sleep 400ms Enter
Sleep 2500ms

# 2. Three agents, three worktrees, in parallel
Type "for a in 1 2 3; do git worktree add -q ../agent-$a -b agent-$a & done; wait" Sleep 400ms Enter
Sleep 7s

# 3. Separate DSNs and sticky ports
Type `grep -h -E 'DATABASE_URL|PORT' ../agent-*/.env | sed -E 's#://([^:]+):[^@]+@#://\1:***@#'` Sleep 400ms Enter
Sleep 3s

# 4. Isolation: write in agent-1, read in agent-2
Type `(cd ../agent-1 && worktree-pg run -- sh -c "psql -qc \"insert into items(name) values ('only-in-1')\" \"\$DATABASE_URL\"")` Enter
Sleep 1s
Type `(cd ../agent-2 && worktree-pg run -- sh -c "psql -Atc \"select count(*) from items where name = 'only-in-1'\" \"\$DATABASE_URL\"")` Enter
Sleep 2500ms

# 5. Warm template: a fourth worktree in about a second and a half
Type "time git worktree add -q ../agent-4 -b agent-4" Sleep 400ms Enter
Sleep 4s

# 6. Overview and teardown
Type "worktree-pg status --all" Sleep 400ms Enter
Sleep 3s
Type "(cd ../agent-4 && worktree-pg down)" Sleep 400ms Enter
Sleep 2s
