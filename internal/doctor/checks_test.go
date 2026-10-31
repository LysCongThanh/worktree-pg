package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// newRepo creates an isolated git repo containing config (if non-empty).
func newRepo(t *testing.T, config string) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if config != "" {
		_ = os.WriteFile(filepath.Join(root, ".worktree-pg.toml"), []byte(config), 0o644)
	}
	return root
}

// byName indexes a report.
func byName(rep Report) map[string]Check {
	m := map[string]Check{}
	for _, c := range rep.Checks {
		m[c.Name] = c
	}
	return m
}

const cfgExternal = "version = 1\nname = \"shop\"\n[template]\nmigrate_cmd = \"true\"\n"

func noEnv(string) string { return "" }

func TestDoctorWithoutAdminDSN(t *testing.T) {
	root := newRepo(t, cfgExternal)
	state := t.TempDir()
	_ = os.WriteFile(filepath.Join(state, "secret.key"), []byte("00"), 0o644)
	_ = os.Chmod(filepath.Join(state, "secret.key"), 0o644) // independent of umask
	rep := Run(context.Background(), Options{Cwd: root, StateDir: state, Home: t.TempDir(), Getenv: noEnv})
	m := byName(rep)
	for name, want := range map[string]string{
		"git": OK, "repository": OK, "config": OK, "state": OK, "registry": OK, "secret key": Fail,
		"docker": Skip, "admin dsn": Fail, "claude hook": Warn, "git hook": Warn,
	} {
		if m[name].Status != want {
			t.Errorf("%s: %+v, want %s", name, m[name], want)
		}
	}
	if m["admin dsn"].Hint == "" {
		t.Error("admin dsn failure needs a hint")
	}
	if err := rep.Err(); errs.CodeOf(err) != CodeDoctorFailed || errs.ExitCode(err) != 4 {
		t.Fatalf("Err: %v", err)
	}
}

func TestDoctorOutsideRepoAndBadConfig(t *testing.T) {
	m := byName(Run(context.Background(), Options{Cwd: t.TempDir(), StateDir: filepath.Join(t.TempDir(), "new"), Getenv: noEnv}))
	if m["repository"].Status != Fail || m["state"].Status != OK || m["admin connection"].Status != Skip {
		t.Fatalf("%+v", m)
	}
	root := newRepo(t, "version = 1\nname = \"Bad Name\"\n")
	m = byName(Run(context.Background(), Options{Cwd: root, StateDir: t.TempDir(), Getenv: noEnv}))
	if m["config"].Status != Fail {
		t.Fatalf("invalid config accepted: %+v", m["config"])
	}
	root = newRepo(t, "")
	m = byName(Run(context.Background(), Options{Cwd: root, StateDir: t.TempDir(), Getenv: noEnv}))
	if m["config"].Status != Fail || m["config"].Hint == "" {
		t.Fatalf("missing config: %+v", m["config"])
	}
}

// TestDoctorAdminFromEnvUnreachable: a DSN pointing at a closed port fails the
// connection check without leaking the password.
func TestDoctorAdminFromEnvUnreachable(t *testing.T) {
	root := newRepo(t, cfgExternal)
	getenv := func(k string) string {
		if k == "WORKTREE_PG_ADMIN_DSN" {
			return "postgres://u:sekret@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2"
		}
		return ""
	}
	m := byName(Run(context.Background(), Options{Cwd: root, StateDir: t.TempDir(), Getenv: getenv}))
	if m["admin dsn"].Status != OK || m["admin dsn"].Detail != "from $WORKTREE_PG_ADMIN_DSN" {
		t.Fatalf("%+v", m["admin dsn"])
	}
	c := m["admin connection"]
	if c.Status != Fail || strings.Contains(c.Detail+c.Hint, "sekret") {
		t.Fatalf("%+v", c)
	}
}
