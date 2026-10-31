//go:build integration

package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/docker"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/names"
	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
)

// testPort is inside the 15400-15499 range reserved for this repository's tests.
const testPort = 15451

func needDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("WTPG_TEST_DOCKER") == "0" || docker.Available(context.Background()) != nil {
		t.Skip("docker not available")
	}
}

func suffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TestInvariant_S12_ManagedLifecycle covers create, readiness over host TCP (D4), loopback-only
// publication (S12), restart of a stopped container and idempotent re-runs.
func TestInvariant_S12_ManagedLifecycle(t *testing.T) {
	needDocker(t)
	ctx := context.Background()
	m, err := NewManaged("postgres:16-alpine", "127.0.0.1", testPort, "pw-"+suffix())
	if err != nil {
		t.Fatal(err)
	}
	m.Container = "wtpg-test-managed-" + suffix()
	m.Volume = m.Container + "-data"
	t.Cleanup(func() { _ = docker.Remove(context.Background(), m.Container, m.Volume) })
	acts, err := m.Ensure(ctx)
	if err != nil || !slices.Equal(acts, []string{ActCreateCluster}) {
		t.Fatalf("create: %v %v", acts, err)
	}
	c, err := docker.Inspect(ctx, m.Container)
	if err != nil || c.HostIP != "127.0.0.1" || c.Port != testPort {
		t.Fatalf("binding %+v %v (invariant S12)", c, err)
	}
	if mk, ok := names.ParseMarker(c.Labels[names.LabelKey]); !ok || mk.Kind != names.KindCluster {
		t.Fatalf("label %q", c.Labels[names.LabelKey])
	}
	a, err := m.Cluster().Connect(ctx)
	if err != nil || !a.Info.Superuser || a.Info.Major != 16 {
		t.Fatalf("connect: %+v %v", a, err)
	}
	a.Close(ctx)
	if out, err := exec.Command("docker", "stop", m.Container).CombinedOutput(); err != nil {
		t.Fatalf("docker stop: %v %s", err, out)
	}
	if acts, err := m.Ensure(ctx); err != nil || !slices.Equal(acts, []string{ActStartCluster}) {
		t.Fatalf("restart: %v %v", acts, err)
	}
	if acts, err := m.Ensure(ctx); err != nil || len(acts) != 0 {
		t.Fatalf("idempotent: %v %v", acts, err)
	}
}

// TestManagedRefusesForeignContainer: an unlabeled container with our name is
// never adopted (invariant S1).
func TestManagedRefusesForeignContainer(t *testing.T) {
	needDocker(t)
	name := "wtpg-test-foreign-" + suffix()
	if out, err := exec.Command("docker", "create", "--name", name, "postgres:16-alpine").CombinedOutput(); err != nil {
		t.Fatalf("docker create: %v %s", err, out)
	}
	t.Cleanup(func() { _ = docker.Remove(context.Background(), name, "") })
	m := &Managed{Image: "postgres:16-alpine", Container: name, BindIP: "127.0.0.1", Port: testPort + 1, Password: "x"}
	if _, err := m.Ensure(context.Background()); errs.CodeOf(err) != errs.CodeForeignResource {
		t.Fatalf("want FOREIGN_RESOURCE, got %v", err)
	}
}

// TestImageVolumes (spike D5): PG18 images declare /var/lib/postgresql.
func TestImageVolumes(t *testing.T) {
	needDocker(t)
	want := map[string]string{"postgres:16-alpine": "/var/lib/postgresql/data", "postgres:18-alpine": "/var/lib/postgresql"}
	for img, path := range want {
		v, err := docker.ImageVolumes(context.Background(), img)
		if err != nil || len(v) != 1 || v[0] != path {
			t.Errorf("%s volumes %v %v", img, v, err)
		}
	}
}

// TestSetupRunsOnce: cluster_setup_cmd runs as the admin and is skipped when Done.
func TestSetupRunsOnce(t *testing.T) {
	app := pgtest.App(t)
	a := pgtest.Connect(t, pgtest.Admins(t, app)[1].DSN)
	cfg := config.Default()
	cfg.Name = app
	cfg.Template.ClusterSetupCmd = `psql -X -q -v ON_ERROR_STOP=1 -c "create role ` + app + `_app nologin"`
	done := false
	s := &Setup{Admin: a, Cfg: cfg, Dir: t.TempDir(), Host: a.Host, StateDir: t.TempDir(), LogKey: "t", Done: func() bool { return done }}
	ctx := context.Background()
	if ran, err := s.Run(ctx); err != nil || !ran {
		t.Fatalf("first run: %v %v", ran, err)
	}
	if r, _ := a.Role(ctx, app+"_app"); r == nil {
		t.Fatal("setup did not create the role")
	}
	done = true
	if ran, err := s.Run(ctx); err != nil || ran {
		t.Fatalf("second run: %v %v", ran, err)
	}
	done = false
	if _, err := s.Run(ctx); errs.CodeOf(err) != errs.CodeSetupFailed || errs.ExitCode(err) != 5 {
		t.Fatalf("failing setup: %v", err)
	}
}
