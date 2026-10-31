// Package engine is the single API used by the CLI, the git/Claude hooks and the
// future MCP server. Request/result structs double as the `--json` schemas
// (`{"schema":"worktree-pg/v1", ...}`), so field names and tags are a contract.
package engine

import (
	"io"
	"os"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/backend"
	"github.com/LysCongThanh/worktree-pg/internal/envfile"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/ports"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
	"github.com/LysCongThanh/worktree-pg/internal/secret"
)

// Schema is the JSON schema identifier of every --json document.
const Schema = "worktree-pg/v1"

// Options configure an Engine. Zero values pick sensible defaults.
type Options struct {
	Cwd        string                        // directory the command runs in ("" = os.Getwd)
	ConfigPath string                        // explicit config file ("" = <toplevel>/.worktree-pg.toml)
	StateDir   string                        // "" = registry.StateDir(Getenv)
	Getenv     func(string) string           // nil = os.Getenv (tests inject)
	Stderr     io.Writer                     // progress / user command echo (nil = os.Stderr)
	Verbose    bool                          // echo user command output live
	Now        func() time.Time              // nil = time.Now
	Backend    func(*Engine) backend.Backend // nil = database backend (set by M2)
}

// Engine holds per-process state: registry store and secret key.
type Engine struct {
	Opts   Options
	Store  *registry.Store
	Secret *secret.Secret
}

// Open applies defaults, opens the state directory and loads the secret key.
func Open(o Options) (*Engine, error) {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "getwd")
		}
		o.Cwd = wd
	}
	if o.StateDir == "" {
		d, err := registry.StateDir(o.Getenv)
		if err != nil {
			return nil, err
		}
		o.StateDir = d
	}
	st, err := registry.Open(o.StateDir)
	if err != nil {
		return nil, err
	}
	sec, err := secret.LoadOrCreate(o.StateDir)
	if err != nil {
		return nil, err
	}
	return &Engine{Opts: o, Store: st, Secret: sec}, nil
}

// notImplemented is returned by milestone stubs until the owning lane lands.
func notImplemented(op string) error {
	return errs.New(errs.NotImplemented, errs.CodeNotImplemented, "%s is not implemented yet", op)
}

// Warning is a non-fatal note surfaced in the envelope's "warnings" array.
type Warning = ports.Warning

// WorktreeRef identifies a worktree in results.
type WorktreeRef struct {
	Path   string `json:"path"`
	Key    string `json:"key"`
	Slug   string `json:"slug"`
	Branch string `json:"branch"`
}

// ClusterRef identifies the cluster serving a worktree.
type ClusterRef struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	PGMajor int    `json:"pg_major"`
}

// TemplateRef describes the template a worktree was (or would be) cloned from.
type TemplateRef struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Built       bool   `json:"built"` // built during this run
	Stale       bool   `json:"stale"` // inputs changed since the clone was made
}

// UpRequest provisions (idempotently) the worktree at Path.
type UpRequest struct {
	Path          string        `json:"path"`
	Wait          time.Duration `json:"wait"` // max wait for a concurrent template build / lease
	ReassignPorts bool          `json:"reassign_ports"`
	DryRun        bool          `json:"dry_run"`
	Origin        string        `json:"origin"` // cli | claude-hook | git-hook
}

// UpResult is `up --json` .result.
type UpResult struct {
	Worktree   WorktreeRef      `json:"worktree"`
	Cluster    ClusterRef       `json:"cluster"`
	Database   string           `json:"database"`
	Role       string           `json:"role"`
	Template   TemplateRef      `json:"template"`
	Ports      map[string]int   `json:"ports"`
	EnvFiles   []envfile.Result `json:"env_files"`
	Actions    []string         `json:"actions"` // empty on an idempotent re-run (S11)
	DurationMS int64            `json:"duration_ms"`
	Warnings   []Warning        `json:"-"` // lifted into the envelope
}

// DownRequest tears down the worktree at Path (or by Key when the dir is gone).
type DownRequest struct {
	Path           string `json:"path"`
	Key            string `json:"key"`
	KeepEnv        bool   `json:"keep_env"`
	RemoveWorktree bool   `json:"remove_worktree"` // also `git worktree remove` (claude hook)
}

// DownResult is `down --json` .result.
type DownResult struct {
	Worktree   WorktreeRef      `json:"worktree"`
	Database   string           `json:"database"`
	Role       string           `json:"role"`
	EnvFiles   []envfile.Result `json:"env_files"`
	Actions    []string         `json:"actions"`
	Orphans    []string         `json:"orphans"` // objects that could not be dropped (exit 1)
	DurationMS int64            `json:"duration_ms"`
	Warnings   []Warning        `json:"-"`
}

// StatusRequest lists the current worktree, or every registered one with All.
type StatusRequest struct {
	Path string `json:"path"`
	All  bool   `json:"all"`
}

// WorktreeStatus is one element of `status --json` .result.
type WorktreeStatus struct {
	Worktree   WorktreeRef     `json:"worktree"`
	Status     string          `json:"status"`
	Cluster    ClusterRef      `json:"cluster"`
	Database   string          `json:"database"`
	Role       string          `json:"role"`
	Ports      map[string]int  `json:"ports"`
	EnvFiles   []string        `json:"env_files"`
	Template   TemplateRef     `json:"template"`
	PathExists bool            `json:"path_exists"`
	Health     *backend.Health `json:"health,omitempty"`
	LastError  string          `json:"last_error,omitempty"`
}

// GCRequest reconciles the registry with `git worktree list`. Apply=false is a
// dry run (the default, invariant S13).
type GCRequest struct {
	Path  string `json:"path"`
	Apply bool   `json:"apply"`
}

// GCItem is one candidate or action of a gc run.
type GCItem struct {
	Kind   string `json:"kind"` // worktree | database | role | template | build
	Name   string `json:"name"`
	Key    string `json:"key,omitempty"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
	Action string `json:"action"` // would_drop | dropped | skipped
}

// GCReport is `gc --json` .result.
type GCReport struct {
	DryRun   bool      `json:"dry_run"`
	Items    []GCItem  `json:"items"`
	Warnings []Warning `json:"-"`
}
