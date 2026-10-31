// Package cluster resolves the PostgreSQL cluster a repository uses: an external
// server reached through the admin DSN, or a tool-managed docker container
// (managed.go). It also runs the once-per-cluster setup command (setup.go).
package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
)

// Cluster kinds (registry.Cluster.Kind).
const (
	KindExternal = "external"
	KindDocker   = "docker"
)

// Cluster is a reachable cluster plus its (secret) admin DSN.
type Cluster struct {
	ID        string
	Kind      string
	Image     string // docker only
	Container string // docker only
	Host      string // admin host as reachable from this machine
	Port      int
	dsn       string
}

// External resolves the external cluster from [cluster].admin_dsn_env / _file.
// Its ID hashes host:port so every repository using that server shares one record.
func External(cfg *config.Config, getenv func(string) string) (*Cluster, error) {
	dsn, err := cfg.ResolveAdminDSN(getenv)
	if err != nil {
		return nil, err
	}
	pc, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errs.New(errs.Prereq, errs.CodeAdminDSNMissing, "admin DSN is not a valid connection string")
	}
	return &Cluster{ID: ExternalID(pc.Host, int(pc.Port)), Kind: KindExternal, Host: pc.Host, Port: int(pc.Port), dsn: dsn}, nil
}

// ExternalID is the registry id of an external cluster: ext-<sha256(host:port)[:12]>.
func ExternalID(host string, port int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", host, port)))
	return "ext-" + hex.EncodeToString(sum[:])[:12]
}

// Connect opens the admin connection.
func (c *Cluster) Connect(ctx context.Context) (*pg.Admin, error) { return pg.Connect(ctx, c.dsn) }

// AdminPassword returns the admin password from the DSN ("" when it has none);
// shared roles mode writes it into DSN vars.
func (c *Cluster) AdminPassword() string {
	pc, err := pgx.ParseConfig(c.dsn)
	if err != nil {
		return ""
	}
	return pc.Password
}

// CheckCaps verifies the admin can do what the roles mode needs (spec §5 Clone):
// CREATEDB always, CREATEROLE for per_worktree. Failures are PG_PRIVILEGE (exit 4).
func CheckCaps(info pg.ServerInfo, rolesMode string) error {
	if info.Superuser {
		return nil
	}
	if !info.CreateDB {
		return errs.New(errs.Prereq, errs.CodePGPrivilege, "admin role %s lacks CREATEDB", info.User).
			WithHint("ALTER ROLE %s CREATEDB, or use another admin DSN", info.User)
	}
	if rolesMode == "per_worktree" && !info.CreateRole {
		return errs.New(errs.Prereq, errs.CodePGPrivilege, "admin role %s lacks CREATEROLE (needed for roles = \"per_worktree\")", info.User).
			WithHint("ALTER ROLE %s CREATEROLE, or set [database] roles = \"shared\"", info.User)
	}
	return nil
}
