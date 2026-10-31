package envfile

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

var update = flag.Bool("update", false, "rewrite golden files")

// goldenVars covers bare, single-quoted and double-quoted renderings.
var goldenVars = []Var{
	{Key: "DATABASE_URL", Value: "postgres://shop_wt_a:p4ss@127.0.0.1:5601/shop_wt_a?sslmode=disable", Secret: true},
	{Key: "PORT", Value: "5602"},
	{Key: "GREETING", Value: "hello world $HOME"},
	{Key: "TRICKY", Value: "it's \"q\" $X `y` \\z"},
	{Key: "MULTI", Value: "a\nb"},
	{Key: "EMPTY", Value: ""},
}

// TestGolden splices goldenVars into every testdata/*.in and compares with *.golden.
// "no_file" has no .in: a missing file behaves like empty input.
func TestGolden(t *testing.T) {
	ins, _ := filepath.Glob("testdata/*.in")
	ins = append(ins, "testdata/no_file.in")
	for _, in := range ins {
		name := strings.TrimSuffix(filepath.Base(in), ".in")
		t.Run(name, func(t *testing.T) {
			old, _ := os.ReadFile(in) // missing -> nil
			out, dups, err := Splice(old, goldenVars)
			got := string(out)
			if err != nil {
				got = "ERROR " + errs.CodeOf(err) + "\n"
			} else if len(dups) > 0 {
				got += "# dup: " + strings.Join(dups, ",") + "\n"
			}
			gold := filepath.Join("testdata", name+".golden")
			if *update {
				_ = os.WriteFile(gold, []byte(got), 0o644)
			}
			want, err := os.ReadFile(gold)
			if err != nil {
				t.Fatalf("missing golden (run go test -update): %v", err)
			}
			if got != string(want) {
				t.Fatalf("mismatch\n--- got\n%q\n--- want\n%q", got, want)
			}
			if err == nil && !strings.HasPrefix(got, "ERROR") {
				again, _, _ := Splice(out, goldenVars)
				if !bytes.Equal(again, out) {
					t.Fatal("splice not idempotent")
				}
				stripped, found, _ := Strip(out)
				if !found || bytes.Contains(stripped, []byte(BeginMarker)) {
					t.Fatal("strip failed")
				}
			}
		})
	}
}

func TestStripRestoresOriginal(t *testing.T) {
	for _, orig := range []string{"A=1\nB=2\n", "A=1\r\nB=2\r\n", ""} {
		out, _, _ := Splice([]byte(orig), goldenVars[:2])
		back, found, err := Strip(out)
		if err != nil || !found || string(back) != orig {
			t.Errorf("Strip(Splice(%q)) = %q, %v, %v", orig, back, found, err)
		}
	}
	if b, found, err := Strip([]byte("A=1\n")); found || err != nil || string(b) != "A=1\n" {
		t.Error("Strip without block changed input")
	}
}

func TestQuoting(t *testing.T) {
	if QuoteShell("") != "''" || QuoteShell("ab") != "ab" || QuoteShell("it's") != `'it'\''s'` {
		t.Fatal("QuoteShell")
	}
	lines := RenderShell([]Var{{Key: "A", Value: "x y"}})
	if lines[0] != "export A='x y'" {
		t.Fatal(lines)
	}
}

func TestWriteAndRemove(t *testing.T) {
	root := t.TempDir()
	vars := goldenVars[:2]
	r, err := Write(root, ".env", vars)
	if err != nil || !r.Changed || !r.Created {
		t.Fatalf("first write: %+v %v", r, err)
	}
	if perm(r.Path) != 0o600 {
		t.Fatalf("perm %o", perm(r.Path))
	}
	r2, err := Write(root, ".env", vars)
	if err != nil || r2.Changed || r2.Created {
		t.Fatalf("second write not idempotent: %+v %v", r2, err)
	}
	if ch, err := Remove(root, ".env", true); err != nil || !ch {
		t.Fatalf("remove: %v %v", ch, err)
	}
	if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
		t.Fatal("created file not deleted when empty")
	}
	user := filepath.Join(root, ".env.local")
	_ = os.WriteFile(user, []byte("MINE=1\n"), 0o644)
	if _, err := Write(root, ".env.local", vars); err != nil {
		t.Fatal(err)
	}
	if ch, err := Remove(root, ".env.local", false); err != nil || !ch {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(user); string(b) != "MINE=1\n" {
		t.Fatalf("user content not restored: %q", b)
	}
	if ch, err := Remove(root, "absent.env", false); err != nil || ch {
		t.Fatal("remove of absent file")
	}
}

func TestWriteRefusesUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "victim"), []byte("X=1\n"), 0o644)
	_ = os.Symlink(filepath.Join(outside, "victim"), filepath.Join(root, ".env"))
	_ = os.Symlink(outside, filepath.Join(root, "linkdir"))
	_ = os.Mkdir(filepath.Join(root, "adir"), 0o755)
	for _, rel := range []string{".env", "linkdir/.env", "../x.env", "/etc/x", "missing/.env", "adir"} {
		_, err := Write(root, rel, goldenVars[:1])
		if errs.CodeOf(err) != errs.CodeEnvTargetUnsafe || errs.ExitCode(err) != 2 {
			t.Errorf("Write(%q) = %v, want ENV_TARGET_UNSAFE", rel, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "victim")); string(b) != "X=1\n" {
		t.Fatal("symlink target modified")
	}
	if _, err := Remove(root, ".env", true); errs.CodeOf(err) != errs.CodeEnvTargetUnsafe {
		t.Fatalf("Remove via symlink: %v", err)
	}
	_ = os.WriteFile(filepath.Join(root, "bad.env"), []byte(BeginMarker+"\n"), 0o600)
	if _, err := Write(root, "bad.env", goldenVars[:1]); errs.CodeOf(err) != errs.CodeEnvBlockCorrupt {
		t.Fatalf("corrupt block: %v", err)
	}
}
