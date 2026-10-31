//go:build e2e

package e2e

import (
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInvariant_S6_SecretsNeverLeak: the worktree role's password (only ever
// printed by `env --format json`) appears nowhere in up/status JSON, the
// registry, the state dir logs, or the argv of worktree-pg and its children.
// The migrate command records its parent's argv to prove the latter.
func TestInvariant_S6_SecretsNeverLeak(t *testing.T) {
	app := newApp(t)
	s, _ := newPGSandbox(t, superDSN(t))
	argvLog := filepath.Join(s.dir, "argv.log")
	s.env = append(s.env, "WTPG_E2E_ARGV_LOG="+argvLog)
	migrate := migrateAll + `; { tr '\0' ' ' < /proc/$PPID/cmdline; echo; tr '\0' ' ' < /proc/$$/cmdline; echo; } >> "$WTPG_E2E_ARGV_LOG"`
	root := s.repo(sampleRepo(app, migrate))
	wt := filepath.Join(s.dir, "secret")
	s.run(root, "", "git", "worktree", "add", "-q", "-b", "secret", wt)

	up := s.run(wt, "", "", "--json", "up")
	if up.code != 0 {
		t.Fatalf("up: %+v", up)
	}
	status := s.run(wt, "", "", "--json", "status")
	env := envelope(t, s.run(wt, "", "", "env", "--format", "json"))
	var pw string
	for _, v := range env["result"].(map[string]any)["vars"].([]any) {
		m := v.(map[string]any)
		if m["key"] == "DATABASE_URL" {
			u, err := url.Parse(m["value"].(string))
			if err != nil {
				t.Fatal(err)
			}
			pw, _ = u.User.Password()
		}
	}
	if len(pw) < 16 {
		t.Fatalf("could not read the worktree password from env json: %q", pw)
	}
	check := func(where, text string) {
		if strings.Contains(text, pw) {
			t.Errorf("password leaked into %s", where)
		}
	}
	check("up JSON", up.stdout+up.stderr)
	check("status JSON", status.stdout+status.stderr)
	argv, err := os.ReadFile(argvLog)
	if err != nil || !strings.Contains(string(argv), "up") {
		t.Fatalf("argv log not written: %v %q", err, argv)
	}
	check("argv", string(argv))
	_ = filepath.WalkDir(filepath.Join(s.dir, "state"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "secret.key" {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		check(p, string(b))
		return nil
	})
	if r := s.run(wt, "", "", "down"); r.code != 0 {
		t.Fatalf("down: %+v", r)
	}
}
