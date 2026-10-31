package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/envfile"
)

// testApp returns an app with captured streams, an isolated state dir and an
// environment without any admin DSN (env overrides individual variables).
func testApp(t *testing.T, stdin string, env map[string]string) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv("WORKTREE_PG_STATE_DIR", t.TempDir())
	var out, errb bytes.Buffer
	a := newApp(strings.NewReader(stdin), &out, &errb)
	a.getenv = func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		if k == "WORKTREE_PG_ADMIN_DSN" {
			return ""
		}
		return os.Getenv(k)
	}
	return a, &out, &errb
}

// cli runs one invocation and returns exit code, stdout and stderr.
func cli(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	a, out, errb := testApp(t, "", nil)
	code := a.run(context.Background(), args)
	return code, out.String(), errb.String()
}

// newRepo creates an isolated git repo with one commit and optional config.
func newRepo(t *testing.T, config string) string {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		t.Setenv(k, v)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(dir, "shop")
	_ = os.Mkdir(root, 0o755)
	if config != "" {
		_ = os.WriteFile(filepath.Join(root, ".worktree-pg.toml"), []byte(config), 0o644)
	}
	_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".claude/\n.env\n"), 0o644)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return root
}

func TestRunDispatchAndUsage(t *testing.T) {
	cases := []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"version"}, 0, "worktree-pg dev"},
		{[]string{"help"}, 0, "Usage: worktree-pg"},
		{[]string{"-h"}, 0, ""},
		{[]string{"up", "-h"}, 0, ""},
		{nil, 3, ""},
		{[]string{"bogus"}, 3, ""},
		{[]string{"up", "--nope"}, 3, ""},
		{[]string{"up", "extra"}, 3, ""},
		{[]string{"status", "--all", "x"}, 3, ""},
		{[]string{"env", "--format", "yaml"}, 3, ""},
		{[]string{"run"}, 3, ""},
		{[]string{"run", "--"}, 3, ""},
		{[]string{"template"}, 3, ""},
		{[]string{"template", "bogus"}, 3, ""},
		{[]string{"hook"}, 3, ""},
		{[]string{"hook", "install", "vim"}, 3, ""},
		{[]string{"claude-hook", "bogus"}, 3, ""},
		{[]string{"git-hook"}, 0, ""}, // git hooks never fail
		{[]string{"git-hook", "post-checkout", "a", "b", "1"}, 0, ""},
	}
	for _, c := range cases {
		code, out, errOut := cli(t, c.args...)
		if code != c.code || !strings.Contains(out, c.out) {
			t.Errorf("%v: exit %d out=%q err=%q", c.args, code, out, errOut)
		}
	}
}

// TestGlobalFlagsBeforeAndAfter: --json works on either side of the command and
// a failure still yields exactly one JSON document on stdout.
func TestGlobalFlagsBeforeAndAfter(t *testing.T) {
	for _, args := range [][]string{{"--json", "version"}, {"version", "--json"}} {
		if code, out, _ := cli(t, args...); code != 0 || !strings.HasPrefix(out, "{") || strings.Count(out, `"schema"`) != 1 {
			t.Errorf("%v: %d %q", args, code, out)
		}
	}
	code, out, _ := cli(t, "--json", "up", "--nope")
	if code != 3 || strings.Count(out, `"schema"`) != 1 || !strings.Contains(out, `"code": "USAGE"`) {
		t.Fatalf("%d %q", code, out)
	}
}

func TestSplitInheritedAndRunEnv(t *testing.T) {
	vars := []envfile.Var{{Key: "PORT", Value: "5600"}, {Key: "DATABASE_URL", Value: "postgres://x"}}
	lookup := func(k string) (string, bool) { return "", k == "PORT" }
	keep, skipped := splitInherited(vars, false, lookup)
	if len(keep) != 1 || keep[0].Key != "DATABASE_URL" || len(skipped) != 1 {
		t.Fatalf("%v %v", keep, skipped)
	}
	if keep, skipped = splitInherited(vars, true, lookup); len(keep) != 2 || len(skipped) != 0 {
		t.Fatal("override")
	}
	base := []string{"PATH=/bin", "PORT=3000"}
	got := strings.Join(RunEnv(base, vars, false), " ")
	if got != "PATH=/bin PORT=3000 DATABASE_URL=postgres://x" {
		t.Fatalf("inherited wins: %s", got)
	}
	got = strings.Join(RunEnv(base, vars, true), " ")
	if got != "PATH=/bin PORT=5600 DATABASE_URL=postgres://x" {
		t.Fatalf("override: %s", got)
	}
}
