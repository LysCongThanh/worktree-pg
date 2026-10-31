// Package hooks adapts worktree-pg to Claude Code (WorktreeCreate/WorktreeRemove
// command hooks) and git (post-checkout), and installs those hooks.
//
// Claude Code contract (spike S8, CC 2.1.316): the hook reads one JSON object on
// stdin and the LAST stdout line of `claude-hook create` is taken as the worktree
// path. Every other byte of output therefore goes to stderr.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"text/template"

	"github.com/LysCongThanh/worktree-pg/internal/engine"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// MaxStdin bounds the hook payload read from stdin (1 MiB).
const MaxStdin = 1 << 20

// InHookEnv is set while worktree-pg itself runs `git worktree add`, so the
// post-checkout hook does not provision a second time.
const InHookEnv = "WORKTREE_PG_IN_HOOK"

// ClaudeInput is the subset of the Claude Code hook payload worktree-pg reads.
// Unknown fields are ignored so newer Claude Code versions stay compatible.
type ClaudeInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Name           string `json:"name"`          // WorktreeCreate
	WorktreePath   string `json:"worktree_path"` // WorktreeRemove
}

// Provisioner is the slice of *engine.Engine the hooks need.
type Provisioner interface {
	Up(ctx context.Context, req engine.UpRequest) (*engine.UpResult, error)
	Down(ctx context.Context, req engine.DownRequest) (*engine.DownResult, error)
}

// Env carries the process context of one hook invocation.
type Env struct {
	Stdin      io.Reader
	Stdout     io.Writer // claude-hook create: only the final path line
	Stderr     io.Writer // everything else
	Getenv     func(string) string
	Cwd        string // process working directory
	ConfigPath string // --config override ("" = <toplevel>/.worktree-pg.toml)
	Engine     Provisioner
	Store      *registry.Store
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ReadClaudeInput parses at most MaxStdin bytes of hook JSON from r.
func ReadClaudeInput(r io.Reader) (ClaudeInput, error) {
	var in ClaudeInput
	b, err := io.ReadAll(io.LimitReader(r, MaxStdin+1))
	if err != nil {
		return in, errs.Wrap(errs.Internal, errs.CodeInternal, err, "read hook input")
	}
	if len(b) > MaxStdin {
		return in, badInput("hook input exceeds %d bytes", MaxStdin)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return in, badInput("empty hook input on stdin")
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return in, errs.Wrap(errs.Config, errs.CodeConfigInvalid, err, "hook input is not a JSON object")
	}
	return in, nil
}

// ValidName enforces `^[A-Za-z0-9._-]+$` without ".." so a worktree name can
// never climb out of worktree_dir.
func ValidName(name string) error {
	if !nameRe.MatchString(name) || strings.Contains(name, "..") || name == "." {
		return badInput("invalid worktree name %q (allowed: letters, digits, . _ -, no \"..\")", name)
	}
	return nil
}

// render expands a [hooks.claude] template ({{.Name}}) for one worktree name.
func render(field, text, name string) (string, error) {
	t, err := template.New(field).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", errs.Wrap(errs.Config, errs.CodeConfigInvalid, err, "hooks.claude.%s", field)
	}
	var b strings.Builder
	if err := t.Execute(&b, struct{ Name string }{name}); err != nil {
		return "", errs.Wrap(errs.Config, errs.CodeConfigInvalid, err, "hooks.claude.%s", field)
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", errs.New(errs.Config, errs.CodeConfigInvalid, "hooks.claude.%s renders empty", field)
	}
	return b.String(), nil
}

func badInput(format string, a ...any) error {
	return errs.New(errs.Config, errs.CodeConfigInvalid, format, a...).
		WithHint("this command is meant to be run by Claude Code; see docs/agents.md")
}

// getenv returns e.Getenv or a function returning "".
func (e Env) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}
