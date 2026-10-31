// Package backend defines how a worktree's database is provisioned. v0.1 ships one
// implementation (database.go: a database + role cloned from the template on a
// shared cluster); v0.3 adds container isolation behind the same interface.
package backend

import (
	"context"

	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/registry"
)

// Backend provisions, destroys and inspects one worktree's database.
// Implementations must be idempotent (invariant S11) and must only touch objects
// whose marker matches the worktree key (invariant S1).
type Backend interface {
	// Ensure creates (or verifies) the database and role described by spec and
	// returns how to reach them. Re-running with the same spec is a no-op.
	Ensure(ctx context.Context, spec Spec) (Endpoint, error)
	// Destroy drops the worktree's database then role (only if markers match).
	Destroy(ctx context.Context, w *registry.Worktree) error
	// Inspect reports the live state of the worktree's objects without changing them.
	Inspect(ctx context.Context, w *registry.Worktree) (Health, error)
}

// Spec is everything Ensure needs; computed by the engine from config + registry.
type Spec struct {
	Key  string // worktree key (names.Key)
	Path string // symlink-resolved worktree root
	App  string // config name
	Slug string

	DB        string // names.Database
	Role      string // names.Role (== DB); in shared mode the owner role is used instead
	OwnerRole string // names.OwnerRole: owns template objects, REASSIGNed to Role
	Template  string // published template database to clone from

	RolesMode           string   // per_worktree | shared
	Strategy            string   // "" | wal_log | file_copy
	RevokePublicConnect bool     // REVOKE CONNECT ON DATABASE FROM PUBLIC
	ConnectRoles        []string // extra [[dsn]] cluster roles granted CONNECT

	Password string       `json:"-"` // HMAC-derived role password (secret.Password); never logged
	Marker   names.Marker // COMMENT written on the database and role
}

// Endpoint is where the provisioned database is reachable.
type Endpoint struct {
	ClusterID string   `json:"cluster_id"`
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	DB        string   `json:"database"`
	Role      string   `json:"role"`
	Password  string   `json:"-"`       // secret: only rendered into env files / env --format json
	Actions   []string `json:"actions"` // e.g. "create_role", "create_database", "reassign_owned"
}

// Health is the observed state of a worktree's objects (status / doctor / gc).
type Health struct {
	DBExists      bool   `json:"db_exists"`
	RoleExists    bool   `json:"role_exists"`
	MarkerOK      bool   `json:"marker_ok"` // markers present and match the worktree key
	Sessions      int    `json:"sessions"`  // live connections to the database
	TemplateStale bool   `json:"template_stale"`
	Detail        string `json:"detail,omitempty"`
}
