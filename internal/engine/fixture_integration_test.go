//go:build integration

package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/pg/pgtest"
)

// testRanges stays inside the 15400-15499 port block reserved for this repo's tests.
const testRanges = `["15460-15499"]`

// repoFixture is a git repository with a .worktree-pg.toml and an Engine whose
// admin DSN comes from an injected environment.
type repoFixture struct {
	t    *testing.T
	app  string
	main string // main worktree path
	env  map[string]string
	e    *Engine
}

// newRepo creates the repository and engine for app, as admin dsn. extra is
// appended to [template].
func newRepo(t *testing.T, app, dsn, extra string) *repoFixture {
	t.Helper()
	root := t.TempDir()
	main := filepath.Join(root, "main")
	cfg := fmt.Sprintf(`version = 1
name = %q
[cluster]
admin_dsn_env = "WTPG_IT_ADMIN"
[template]
inputs = ["db"]
migrate_cmd = "psql -X -q -v ON_ERROR_STOP=1 -f db/m.sql"
cluster_setup_cmd = "psql -X -q -v ON_ERROR_STOP=1 -c 'create role %s_reader nologin'"
%s
[[dsn]]
var = "DATABASE_URL"
params = "sslmode=disable"
[[dsn]]
var = "READER_URL"
role = "%s_reader"
password_env = "READER_PW"
format = "kv"
[ports]
ranges = %s
[ports.vars]
PORT = ""
ADMIN_PORT = ""
[env]
files = [".env"]
[env.vars]
APP_URL = 'http://127.0.0.1:{{Port "PORT"}}'
GOOSE_DBSTRING = '{{DSN "DATABASE_URL"}}'
NAME = "{{.App}}_{{.Slug}}"
`, app, app, extra, app, testRanges)
	files := map[string]string{
		".worktree-pg.toml": cfg,
		".gitignore":        ".env\n",
		"db/m.sql":          "create table items(id serial primary key, name text);\ninsert into items(name) values ('seed');\n",
	}
	for rel, body := range files {
		p := filepath.Join(main, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &repoFixture{t: t, app: app, main: main, env: map[string]string{"WTPG_IT_ADMIN": dsn, "READER_PW": "readerpw"}}
	f.git(main, "init", "-q", "-b", "main")
	f.git(main, "add", "-A")
	f.git(main, "-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-q", "-m", "init")
	e, err := Open(Options{Cwd: main, StateDir: filepath.Join(root, "state"), Getenv: f.getenv, Stderr: testWriter{t}})
	if err != nil {
		t.Fatal(err)
	}
	f.e = e
	return f
}

func (f *repoFixture) getenv(k string) string {
	if v, ok := f.env[k]; ok {
		return v
	}
	if k == "PATH" || k == "HOME" {
		return os.Getenv(k)
	}
	return ""
}

// git runs git in dir or fails the test.
func (f *repoFixture) git(dir string, args ...string) string {
	f.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// worktree adds a linked worktree named name next to main and returns its path.
func (f *repoFixture) worktree(name string) string {
	f.t.Helper()
	p := filepath.Join(filepath.Dir(f.main), name)
	f.git(f.main, "worktree", "add", "-q", "-b", name, p)
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		f.t.Fatal(err)
	}
	return real
}

// adminDSNs returns the superuser and nosuper admin DSNs for app.
func adminDSNs(t *testing.T, app string) map[string]string {
	a := pgtest.Admins(t, app)
	return map[string]string{a[0].Name: a[0].DSN, a[1].Name: a[1].DSN}
}

// up runs Up for path and fails the test on error.
func (f *repoFixture) up(path string) *UpResult {
	f.t.Helper()
	res, err := f.e.Up(context.Background(), UpRequest{Path: path, Origin: "cli"})
	if err != nil {
		f.t.Fatalf("up %s: %v (hint: %s)", path, err, errsHint(err))
	}
	return res
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}
