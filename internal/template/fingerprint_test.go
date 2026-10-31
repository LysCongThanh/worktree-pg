package template

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
)

func setup(t *testing.T) (string, *config.Config) {
	t.Helper()
	root := t.TempDir()
	mk := func(rel, body string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("db/migrations/001_init.sql", "create table a(id int);\n")
	mk("db/migrations/002_b.sql", "create table b(id int);\n")
	mk("db/migrations/.git/HEAD", "ignored\n")
	mk("db/roles.sql", "create role app;\n")
	c := config.Default()
	c.Name = "shop"
	c.Template.Inputs = []string{"db/migrations"}
	c.Template.MigrateCmd = "goose up"
	return root, c
}

var srv16 = pg.ServerInfo{VersionNum: 160015, Major: 16}

func TestFingerprintStable(t *testing.T) {
	root, c := setup(t)
	a, err := Fingerprint(root, c, srv16, "")
	if err != nil {
		t.Fatal(err)
	}
	// mtime changes and .git contents must not matter
	later := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(root, "db/migrations/001_init.sql"), later, later)
	_ = os.WriteFile(filepath.Join(root, "db/migrations/.git/HEAD"), []byte("changed"), 0o644)
	c.Template.Extensions = []string{"citext", "pgcrypto"}
	b, _ := Fingerprint(root, c, srv16, "")
	c.Template.Extensions = []string{"pgcrypto", "citext"} // order-insensitive
	b2, _ := Fingerprint(root, c, srv16, "")
	if b != b2 || len(a) != 64 {
		t.Fatalf("unstable: %s %s", b, b2)
	}
	c.Template.Extensions = nil
	if a2, _ := Fingerprint(root, c, srv16, ""); a2 != a {
		t.Fatal("mtime or .git changed the fingerprint")
	}
	lines, _ := Lines(root, c, srv16, "")
	if lines[0] != FPVersion || len(lines) != 11 {
		t.Fatalf("lines: %q", lines)
	}
}

func TestFingerprintChanges(t *testing.T) {
	root, c := setup(t)
	base, _ := Fingerprint(root, c, srv16, "")
	mutations := map[string]func(){
		"content":   func() { _ = os.WriteFile(filepath.Join(root, "db/migrations/002_b.sql"), []byte("x"), 0o644) },
		"new file":  func() { _ = os.WriteFile(filepath.Join(root, "db/migrations/003.sql"), []byte("x"), 0o644) },
		"symlink":   func() { _ = os.Symlink("001_init.sql", filepath.Join(root, "db/migrations/link.sql")) },
		"migrate":   func() { c.Template.MigrateCmd = "goose -v up" },
		"seed":      func() { c.Template.SeedCmd = "psql -f seed.sql" },
		"roles":     func() { c.Database.Roles = "shared" },
		"setup cmd": func() { c.Template.ClusterSetupCmd = "psql -f db/roles.sql" },
		"setup in": func() {
			c.Template.ClusterSetupCmd = "psql -f db/roles.sql"
			c.Template.ClusterSetupInputs = []string{"db/roles.sql"}
		},
	}
	seen := map[string]string{base: "base"}
	for name, mutate := range mutations {
		root, c = setup(t)
		mutate()
		fp, err := Fingerprint(root, c, srv16, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if prev, dup := seen[fp]; dup {
			t.Errorf("%s produced same fingerprint as %s", name, prev)
		}
		seen[fp] = name
	}
	root, c = setup(t)
	img, _ := Fingerprint(root, c, srv16, "postgres:16")
	pg17, _ := Fingerprint(root, c, pg.ServerInfo{Major: 17}, "")
	if img == base || pg17 == base || img == pg17 {
		t.Fatal("image/major not part of fingerprint")
	}
}

func TestFingerprintMissingInput(t *testing.T) {
	root, c := setup(t)
	c.Template.Inputs = append(c.Template.Inputs, "db/nope")
	_, err := Fingerprint(root, c, srv16, "")
	if errs.CodeOf(err) != errs.CodeInputMissing || errs.ExitCode(err) != 3 {
		t.Fatalf("want INPUT_MISSING exit 3, got %v", err)
	}
	c.Template.Inputs = nil
	c.Template.ClusterSetupCmd = "x"
	c.Template.ClusterSetupInputs = []string{"missing.sql"}
	if _, err := SetupFingerprint(root, c); errs.CodeOf(err) != errs.CodeInputMissing {
		t.Fatalf("setup input: %v", err)
	}
	if fp, _ := SetupFingerprint(root, config.Default()); fp != "" {
		t.Fatal("empty setup cmd must give empty fp")
	}
}
