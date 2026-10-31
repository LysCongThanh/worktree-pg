package hooks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/engine"
	"github.com/LysCongThanh/worktree-pg/internal/gitx"
)

// isolateGit makes git ignore the developer's global/system config.
func isolateGit(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		t.Setenv(k, v)
	}
}

// newRepo creates a repo "shop" with one commit. config (may be "") is written
// to .worktree-pg.toml and committed so linked worktrees see it too.
func newRepo(t *testing.T, config string) string {
	t.Helper()
	isolateGit(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(dir, "shop")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q", "-b", "main")
	write(t, filepath.Join(root, ".gitignore"), ".env\n*.local\n.claude/\n")
	if config != "" {
		write(t, filepath.Join(root, ".worktree-pg.toml"), config)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "init")
	return root
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Git(context.Background(), dir, nil, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// minimalConfig is valid without any cluster being reachable.
const minimalConfig = `version = 1
name = "shop"
[template]
migrate_cmd = "true"
`

// realEngine opens the real engine with an isolated state dir and no admin DSN,
// so provisioning fails before touching any server (exercises rollback paths).
func realEngine(t *testing.T, cwd string) *engine.Engine {
	t.Helper()
	e, err := engine.Open(engine.Options{
		Cwd: cwd, StateDir: t.TempDir(), Getenv: func(string) string { return "" }, Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// hookEnv builds an Env reading payload from stdin.
func hookEnv(root, payload string, p Provisioner, e *engine.Engine) (Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	env := Env{
		Stdin: strings.NewReader(payload), Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string {
			if k == "CLAUDE_PROJECT_DIR" {
				return root
			}
			return ""
		},
		Cwd: root, Engine: p,
	}
	if e != nil {
		env.Store = e.Store
	}
	return env, &out, &errb
}

// recorder is a Provisioner that records requests and succeeds. It stands in for
// the engine only where the hook's own git/stdout behaviour is under test; the
// database path is covered by the real engine in test/e2e.
type recorder struct {
	ups   []engine.UpRequest
	downs []engine.DownRequest
}

func (r *recorder) Up(_ context.Context, req engine.UpRequest) (*engine.UpResult, error) {
	r.ups = append(r.ups, req)
	return &engine.UpResult{}, nil
}

func (r *recorder) Down(_ context.Context, req engine.DownRequest) (*engine.DownResult, error) {
	r.downs = append(r.downs, req)
	return &engine.DownResult{}, nil
}

// lastLine returns the last non-empty line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}
