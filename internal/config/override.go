package config

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"
)

// LocalSuffix turns `.worktree-pg.toml` into the uncommitted overlay `.worktree-pg.local.toml`.
const LocalSuffix = ".local.toml"

// Default returns a Config pre-filled with documented defaults. TOML decoding only
// overwrites keys present in the file, so defaults survive for omitted keys.
func Default() *Config {
	return &Config{
		Cluster: Cluster{
			Source:        "external",
			AdminDSNEnv:   "WORKTREE_PG_ADMIN_DSN",
			MaintenanceDB: "postgres",
		},
		Database: Database{Roles: "per_worktree", Host: "127.0.0.1", RevokePublicConnect: true},
		Template: Template{Keep: 3, Timeout: Duration{10 * time.Minute}},
		Ports:    Ports{Ranges: []string{"5600-5999"}},
		Env:      Env{Files: []string{".env"}, OnDuplicate: "warn"},
		Hooks: Hooks{
			Claude: ClaudeHook{
				WorktreeDir: ".claude/worktrees/{{.Name}}", Branch: "worktree-{{.Name}}", Base: "origin-default",
				CopyWorktreeInclude: true, OnUpFailure: "fail", RemoveForce: true,
			},
			Git: GitHook{AutoUp: true},
		},
	}
}

// normalize fills per-entry defaults that TOML cannot express for array tables:
// a missing [[dsn]] list becomes DATABASE_URL for the worktree role, and each
// entry defaults to role "@owner" and format "url".
func (c *Config) normalize() {
	if len(c.DSN) == 0 {
		c.DSN = []DSN{{Var: "DATABASE_URL", Role: "@owner", Format: "url", Params: "sslmode=disable"}}
	}
	for i := range c.DSN {
		if c.DSN[i].Role == "" {
			c.DSN[i].Role = "@owner"
		}
		if c.DSN[i].Format == "" {
			c.DSN[i].Format = "url"
		}
	}
}

// LocalPath returns the overlay path for a config path.
func LocalPath(path string) string {
	return strings.TrimSuffix(path, ".toml") + LocalSuffix
}

// applyLocal decodes the optional local overlay on top of c. Overlay semantics
// follow TOML decoding into an existing value: scalars and arrays replace, tables
// (maps such as [env.vars]) merge key by key.
func applyLocal(path string, c *Config) error {
	lp := LocalPath(path)
	if _, err := os.Stat(lp); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return decodeFile(lp, c)
}

// ResolveAdminDSN returns the admin DSN from the configured env var or 0600 file.
// The DSN is never accepted on argv (invariant S6). getenv is injectable for tests.
func (c *Config) ResolveAdminDSN(getenv func(string) string) (string, error) {
	if v := strings.TrimSpace(getenv(c.Cluster.AdminDSNEnv)); c.Cluster.AdminDSNEnv != "" && v != "" {
		return v, nil
	}
	if c.Cluster.AdminDSNFile != "" {
		return readSecretFile(c.Cluster.AdminDSNFile)
	}
	return "", errAdminMissing(c.Cluster.AdminDSNEnv)
}
