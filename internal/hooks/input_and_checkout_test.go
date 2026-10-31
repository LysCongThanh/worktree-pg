package hooks

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

func TestReadClaudeInput(t *testing.T) {
	in, err := ReadClaudeInput(strings.NewReader(createPayload))
	if err != nil || in.Name != "feat-x" || in.HookEventName != "WorktreeCreate" || in.SessionID != "s1" {
		t.Fatalf("%+v %v", in, err)
	}
	big := `{"name":"` + strings.Repeat("a", MaxStdin) + `"}`
	for _, bad := range []string{"", "  \n", "{", big} {
		if _, err := ReadClaudeInput(strings.NewReader(bad)); errs.ExitCode(err) != 3 {
			t.Errorf("accepted %.20q: %v", bad, err)
		}
	}
}

func TestValidName(t *testing.T) {
	for name, ok := range map[string]bool{
		"feat-x": true, "a.b_c-1": true, "A": true,
		"": false, ".": false, "..": false, "a..b": false, "a/b": false, "a b": false, "é": false,
	} {
		if (ValidName(name) == nil) != ok {
			t.Errorf("ValidName(%q) ok=%v", name, !ok)
		}
	}
}

// TestGitPostCheckout: provisions only brand-new branch checkouts of configured
// repos, never when worktree-pg runs git itself, and always returns 0.
func TestGitPostCheckout(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, minimalConfig)
	bare := newRepo(t, "")
	off := filepath.Join(filepath.Dir(bare), "off")
	write(t, filepath.Join(off, "x"), "")
	null := strings.Repeat("0", 40)
	sha := strings.Repeat("a", 40)
	cases := []struct {
		name  string
		cwd   string
		args  []string
		inHk  bool
		wants int // expected Up calls
	}{
		{"new worktree", root, []string{null, sha, "1"}, false, 1},
		{"sha256 null", root, []string{strings.Repeat("0", 64), sha, "1"}, false, 1},
		{"branch switch", root, []string{sha, sha, "1"}, false, 0},
		{"file checkout", root, []string{null, sha, "0"}, false, 0},
		{"inside our own add", root, []string{null, sha, "1"}, true, 0},
		{"no config", bare, []string{null, sha, "1"}, false, 0},
		{"not a repo", off, []string{null, sha, "1"}, false, 0},
		{"bad args", root, []string{"x"}, false, 0},
	}
	for _, c := range cases {
		rec := &recorder{}
		var errb bytes.Buffer
		env := Env{Stderr: &errb, Cwd: c.cwd, Getenv: func(k string) string {
			if k == InHookEnv && c.inHk {
				return "1"
			}
			return ""
		}}
		code := GitPostCheckout(ctx, env, c.args, func() (Provisioner, error) { return rec, nil })
		if code != 0 || len(rec.ups) != c.wants {
			t.Errorf("%s: code %d, ups %d", c.name, code, len(rec.ups))
		}
		if c.wants == 1 && (rec.ups[0].Origin != "git-hook" || rec.ups[0].Path != root) {
			t.Errorf("%s: %+v", c.name, rec.ups[0])
		}
	}
	// a failing engine still exits 0 and explains on stderr
	var errb bytes.Buffer
	env := Env{Stderr: &errb, Cwd: root}
	code := GitPostCheckout(ctx, env, []string{null, sha, "1"}, func() (Provisioner, error) { return nil, errors.New("boom") })
	if code != 0 || !strings.Contains(errb.String(), "checkout kept") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
	off2 := newRepo(t, minimalConfig+"[hooks.git]\nauto_up = false\n")
	rec := &recorder{}
	GitPostCheckout(ctx, Env{Stderr: &errb, Cwd: off2}, []string{null, sha, "1"}, func() (Provisioner, error) { return rec, nil })
	if len(rec.ups) != 0 {
		t.Fatal("auto_up = false ignored")
	}
}
