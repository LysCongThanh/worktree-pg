package cluster

import (
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
)

func TestLoopbackIP(t *testing.T) {
	ok := map[string]string{"localhost": "127.0.0.1", "127.0.0.1": "127.0.0.1", "::1": "::1", "127.0.0.2": "127.0.0.2"}
	for in, want := range ok {
		if got, err := LoopbackIP(in); err != nil || got != want {
			t.Errorf("LoopbackIP(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"0.0.0.0", "192.168.1.5", "db.example.com", "", "::"} {
		if _, err := LoopbackIP(bad); errs.CodeOf(err) != errs.CodeConfigInvalid {
			t.Errorf("LoopbackIP(%q) accepted (invariant S12)", bad)
		}
	}
}

func TestCheckCaps(t *testing.T) {
	cases := []struct {
		info  pg.ServerInfo
		roles string
		code  string
	}{
		{pg.ServerInfo{Superuser: true}, "per_worktree", ""},
		{pg.ServerInfo{CreateDB: true, CreateRole: true}, "per_worktree", ""},
		{pg.ServerInfo{CreateDB: true}, "shared", ""},
		{pg.ServerInfo{CreateDB: true}, "per_worktree", errs.CodePGPrivilege},
		{pg.ServerInfo{CreateRole: true}, "shared", errs.CodePGPrivilege},
	}
	for i, c := range cases {
		err := CheckCaps(c.info, c.roles)
		if errs.CodeOf(err) != c.code || (err != nil && errs.ExitCode(err) != 4) {
			t.Errorf("case %d: %v", i, err)
		}
	}
}

func TestExternalResolve(t *testing.T) {
	cfg := config.Default()
	env := map[string]string{"WORKTREE_PG_ADMIN_DSN": "postgres://adm:s3cret@127.0.0.1:5544/postgres"}
	c, err := External(cfg, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindExternal || c.Port != 5544 || c.ID != ExternalID("127.0.0.1", 5544) || len(c.ID) != 16 {
		t.Fatalf("%+v", c)
	}
	if c.AdminPassword() != "s3cret" {
		t.Fatal("admin password")
	}
	if _, err := External(cfg, func(string) string { return "" }); errs.CodeOf(err) != errs.CodeAdminDSNMissing {
		t.Fatalf("missing DSN: %v", err)
	}
}

func TestManagedNamesAndDSN(t *testing.T) {
	m, err := NewManaged("postgres:18-alpine", "localhost", 15460, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if m.Container != "wtpg-cluster-postgres-18-alpine" || m.Volume != m.Container+"-data" || m.BindIP != "127.0.0.1" {
		t.Fatalf("%+v", m)
	}
	if m.DSN() != "postgres://postgres:pw@127.0.0.1:15460/postgres?sslmode=disable" {
		t.Fatal(m.DSN())
	}
	if c := m.Cluster(); c.ID != m.Container || c.Kind != KindDocker || c.Port != 15460 {
		t.Fatalf("%+v", c)
	}
	if _, err := NewManaged("postgres:16", "10.0.0.1", 1, "pw"); err == nil {
		t.Fatal("non-loopback host accepted")
	}
}
