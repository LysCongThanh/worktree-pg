// Package config loads and validates the committed `.worktree-pg.toml` file
// (plus an optional uncommitted `.worktree-pg.local.toml` overlay).
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// FileName is the committed config file at the repository root.
const FileName = ".worktree-pg.toml"

// Config is the full `.worktree-pg.toml` document.
type Config struct {
	Version  int      `toml:"version"`
	Name     string   `toml:"name"`
	Cluster  Cluster  `toml:"cluster"`
	Database Database `toml:"database"`
	Template Template `toml:"template"`
	DSN      []DSN    `toml:"dsn"`
	Ports    Ports    `toml:"ports"`
	Env      Env      `toml:"env"`
	Hooks    Hooks    `toml:"hooks"`

	Path string `toml:"-"` // absolute path of the loaded file
	Root string `toml:"-"` // directory containing the file (repo/worktree root)
}

// Cluster selects where databases live.
type Cluster struct {
	Source        string `toml:"source"`         // external | docker
	AdminDSNEnv   string `toml:"admin_dsn_env"`  // env var holding the admin DSN (never a CLI flag)
	AdminDSNFile  string `toml:"admin_dsn_file"` // file holding the admin DSN, must be 0600
	Image         string `toml:"image"`          // docker only
	MaintenanceDB string `toml:"maintenance_db"`
}

// Database controls clone creation.
type Database struct {
	Roles               string `toml:"roles"`    // per_worktree | shared
	Strategy            string `toml:"strategy"` // "" | wal_log | file_copy
	Host                string `toml:"host"`
	RevokePublicConnect bool   `toml:"revoke_public_connect"`
}

// Template controls the migrated template database.
type Template struct {
	Inputs             []string `toml:"inputs"`
	PrepareCmd         string   `toml:"prepare_cmd"`
	MigrateCmd         string   `toml:"migrate_cmd"`
	SeedCmd            string   `toml:"seed_cmd"`
	Extensions         []string `toml:"extensions"`
	ClusterSetupCmd    string   `toml:"cluster_setup_cmd"`
	ClusterSetupInputs []string `toml:"cluster_setup_inputs"`
	PostCloneCmd       string   `toml:"post_clone_cmd"`
	Keep               int      `toml:"keep"`
	Timeout            Duration `toml:"timeout"`
}

// DSN describes one connection-string variable written to env files.
type DSN struct {
	Var         string `toml:"var"`
	Role        string `toml:"role"`         // "@owner" = the worktree role, else a cluster-wide role name
	PasswordEnv string `toml:"password_env"` // env var with that role's password (cluster roles only)
	Format      string `toml:"format"`       // url | kv
	Params      string `toml:"params"`
	Host        string `toml:"host"`
}

// Ports configures dev port allocation.
type Ports struct {
	Ranges   []string          `toml:"ranges"`
	Reserved []int             `toml:"reserved"`
	Vars     map[string]string `toml:"vars"` // var -> range override ("" = default ranges)
}

// Env configures managed env-file blocks.
type Env struct {
	Files       []string          `toml:"files"`
	OnDuplicate string            `toml:"on_duplicate"` // warn | error
	Vars        map[string]string `toml:"vars"`         // text/template values
}

// Hooks configures the Claude Code and git adapters.
type Hooks struct {
	Claude ClaudeHook `toml:"claude"`
	Git    GitHook    `toml:"git"`
}

// ClaudeHook configures WorktreeCreate/WorktreeRemove handling.
type ClaudeHook struct {
	WorktreeDir         string `toml:"worktree_dir"`
	Branch              string `toml:"branch"`
	Base                string `toml:"base"` // origin-default | head | <ref>
	CopyWorktreeInclude bool   `toml:"copy_worktreeinclude"`
	OnUpFailure         string `toml:"on_up_failure"` // fail | keep
	RemoveForce         bool   `toml:"remove_force"`
}

// GitHook configures the post-checkout adapter.
type GitHook struct {
	AutoUp bool `toml:"auto_up"`
}

// Duration is a time.Duration decoded from a TOML string such as "10m".
type Duration struct{ time.Duration }

// UnmarshalText parses Go duration syntax.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

// MarshalText renders Go duration syntax.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Load reads path, applies defaults and the optional local overlay, and validates
// the result. Errors are errs.Config (exit 3).
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errs.Wrap(errs.Config, errs.CodeConfigInvalid, err, "config path")
	}
	c := Default()
	if err := decodeFile(abs, c); err != nil {
		return nil, err
	}
	if err := applyLocal(abs, c); err != nil {
		return nil, err
	}
	c.Path, c.Root = abs, filepath.Dir(abs)
	c.normalize()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Find loads FileName from dir (typically the worktree toplevel).
func Find(dir string) (*Config, error) {
	p := filepath.Join(dir, FileName)
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return nil, errs.New(errs.Config, errs.CodeConfigNotFound, "no %s in %s", FileName, dir).
			WithHint("create one from examples/ in the worktree-pg repository")
	}
	return Load(p)
}

// decodeFile decodes one TOML file on top of c, rejecting unknown keys.
func decodeFile(path string, c *Config) error {
	md, err := toml.DecodeFile(path, c)
	if err != nil {
		return errs.Wrap(errs.Config, errs.CodeConfigInvalid, err, "parse %s", path)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return errs.New(errs.Config, errs.CodeConfigInvalid, "%s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	return nil
}
