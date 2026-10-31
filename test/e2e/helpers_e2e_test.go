//go:build e2e

// Package e2e runs the built worktree-pg binary against real git repositories
// and, when WTPG_TEST_ADMIN_DSN is set, a real PostgreSQL server.
//
//	make e2e                                 # builds bin/worktree-pg, sets WTPG_E2E_BIN
//	WTPG_TEST_ADMIN_DSN=postgres://... make e2e
package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// bin returns the binary under test: $WTPG_E2E_BIN or a fresh `go build`.
func bin(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		if p := os.Getenv("WTPG_E2E_BIN"); p != "" {
			binPath = p
			return
		}
		dir, err := os.MkdirTemp("", "wtpg-e2e-bin-")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "worktree-pg")
		out, err := exec.Command("go", "build", "-o", binPath, "../..").CombinedOutput()
		if err != nil {
			buildErr = &buildError{err, string(out)}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

type buildError struct {
	err error
	out string
}

func (b *buildError) Error() string { return b.err.Error() + ": " + b.out }

// sandbox is an isolated environment: own state dir, git config, PATH with the
// binary first (hooks call `worktree-pg` by name) and optionally the admin DSN.
type sandbox struct {
	t   *testing.T
	env []string
	dir string
}

func newSandbox(t *testing.T, withDSN bool) *sandbox {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	env := []string{
		"PATH=" + filepath.Dir(bin(t)) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + dir, "WORKTREE_PG_STATE_DIR=" + filepath.Join(dir, "state"),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	}
	if withDSN {
		env = append(env, "WORKTREE_PG_ADMIN_DSN="+os.Getenv("WTPG_TEST_ADMIN_DSN"))
	}
	return &sandbox{t: t, env: env, dir: dir}
}

// result of one process run.
type result struct {
	code           int
	stdout, stderr string
}

// run executes name (the binary when name == "") in dir with stdin.
func (s *sandbox) run(dir, stdin, name string, args ...string) result {
	s.t.Helper()
	if name == "" {
		name = bin(s.t)
	}
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env, cmd.Stdin = dir, s.env, strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		s.t.Fatalf("%s %v: %v", name, args, err)
	}
	return result{code, o.String(), e.String()}
}

// repo creates repository "shop" with files (path -> content) committed.
func (s *sandbox) repo(files map[string]string) string {
	s.t.Helper()
	root := filepath.Join(s.dir, "shop")
	for p, body := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		if r := s.run(root, "", "git", args...); r.code != 0 {
			s.t.Fatalf("git %v: %s", args, r.stderr)
		}
	}
	return root
}

// envelope decodes a --json document.
func envelope(t *testing.T, r result) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s\nstderr: %s", err, r.stdout, r.stderr)
	}
	return m
}

// skipIfNotImplemented skips when the engine milestone behind a command has not
// landed yet (the CLI reports NOT_IMPLEMENTED); any other failure still fails.
func skipIfNotImplemented(t *testing.T, r result) {
	t.Helper()
	if strings.Contains(r.stdout+r.stderr, "NOT_IMPLEMENTED") {
		t.Skip("engine milestone not implemented yet: " + strings.TrimSpace(r.stderr+r.stdout))
	}
}

// lastLine returns the last non-empty stdout line.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// config is a fixture project migrated with psql (the "migrator").
const config = `version = 1
name = "shop"
[template]
inputs = ["db"]
migrate_cmd = "psql -X -q -v ON_ERROR_STOP=1 -f db/schema.sql"
[[dsn]]
var = "DATABASE_URL"
role = "@owner"
[ports]
ranges = ["15500-15599"]
[ports.vars]
PORT = ""
[env]
files = [".env"]
`

const schema = "CREATE TABLE items (id serial PRIMARY KEY, name text NOT NULL);\nINSERT INTO items(name) VALUES ('seed');\n"
