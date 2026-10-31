// Package pg wraps the admin connection to a PostgreSQL cluster (pgx, single
// connection, no pool). DDL runs through PgConn().Exec (simple protocol) because
// CREATE/DROP DATABASE refuse transaction blocks (spike S4).
//
// This file defines the stable types shared with other packages. Behaviour lives
// in conn.go, database.go, role.go, sqlerr.go, dsn.go and advisory.go (milestone M2).
package pg

import "github.com/jackc/pgx/v5"

// AdvisoryClass is the first key of template-build advisory locks ("wtpg").
const AdvisoryClass int32 = 0x77747067

// ServerInfo describes the server and the admin role's capabilities.
type ServerInfo struct {
	VersionNum int    `json:"server_version_num"` // e.g. 160015
	Major      int    `json:"pg_major"`           // VersionNum / 10000
	User       string `json:"user"`               // current_user of the admin connection
	Superuser  bool   `json:"rolsuper"`
	CreateDB   bool   `json:"rolcreatedb"`
	CreateRole bool   `json:"rolcreaterole"`
}

// NeedsSetGrant reports whether role grants to the admin must spell out
// WITH INHERIT TRUE, SET TRUE: on PG16+ a CREATEROLE admin only receives ADMIN
// OPTION on roles it creates, and CREATE DATABASE ... OWNER r then fails with
// "must be able to SET ROLE" (spike S1).
func (s ServerInfo) NeedsSetGrant() bool { return s.Major >= 16 && !s.Superuser }

// Cred is everything needed to build a connection string for one role. Password
// is a secret: never log it, never put it in state.json or argv (invariant S6).
type Cred struct {
	Host     string
	Port     int
	User     string
	Password string `json:"-"`
	Database string
	Params   string // URL query, e.g. "sslmode=disable"
}

// Admin is an open admin connection plus the capabilities probed at connect time.
type Admin struct {
	Info ServerInfo
	Host string // host as reachable from this machine
	Port int

	conn *pgx.Conn
}

// Conn exposes the underlying connection to sibling packages' tests and M2 code.
func (a *Admin) Conn() *pgx.Conn { return a.conn }
