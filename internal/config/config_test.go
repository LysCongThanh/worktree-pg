package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/ports"
)

var eph = ports.Range{Lo: 32768, Hi: 60999}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimal = "version = 1\nname = \"shop\"\n[template]\nmigrate_cmd = \"true\"\n"

func TestExamplesParse(t *testing.T) {
	matches, _ := filepath.Glob("../../examples/*/.worktree-pg.toml")
	if len(matches) != 3 {
		t.Fatalf("want 3 examples, found %v", matches)
	}
	for _, m := range matches {
		c, err := Load(m)
		if err != nil {
			t.Errorf("%s: %v", m, err)
			continue
		}
		if c.Root != filepath.Dir(c.Path) || len(c.DSN) == 0 {
			t.Errorf("%s: bad root/dsn", m)
		}
	}
	c, _ := Load("../../examples/multi-dsn-rls/.worktree-pg.toml")
	if c == nil || len(c.DSN) != 3 || c.DSN[2].Format != "kv" || c.DSN[1].Format != "url" || len(c.Ports.Vars) != 3 {
		t.Fatalf("multi-dsn example decoded wrong: %+v", c)
	}
}

func TestDefaultsAndOverlay(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, FileName, minimal+"[env.vars]\nA = \"1\"\n")
	write(t, dir, ".worktree-pg.local.toml", "[database]\nhost = \"localhost\"\n[env.vars]\nB = \"2\"\n[ports]\nranges = [\"7000-7100\"]\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Cluster.Source != "external" || c.Database.Roles != "per_worktree" || !c.Database.RevokePublicConnect ||
		c.Template.Keep != 3 || c.Template.Timeout.Duration != 10*time.Minute || c.Hooks.Claude.OnUpFailure != "fail" {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if c.Database.Host != "localhost" || c.Env.Vars["A"] != "1" || c.Env.Vars["B"] != "2" || c.Ports.Ranges[0] != "7000-7100" || len(c.Ports.Ranges) != 1 {
		t.Fatalf("overlay not applied: %+v", c)
	}
	if c.DSN[0].Var != "DATABASE_URL" || c.DSN[0].Role != "@owner" {
		t.Fatalf("default dsn missing: %+v", c.DSN)
	}
}

func TestInvalidConfigs(t *testing.T) {
	cases := map[string]string{
		"unknown key":      minimal + "bogus = 1\n",
		"bad toml":         "version = ",
		"bad version":      strings.Replace(minimal, "version = 1", "version = 2", 1),
		"bad name":         strings.Replace(minimal, "shop", "Shop-1", 1),
		"no migrate":       "version = 1\nname = \"shop\"\n",
		"bad source":       minimal + "[cluster]\nsource = \"k8s\"\n",
		"docker no image":  minimal + "[cluster]\nsource = \"docker\"\n",
		"bad roles":        minimal + "[database]\nroles = \"x\"\n",
		"bad strategy":     minimal + "[database]\nstrategy = \"fast\"\n",
		"ephemeral range":  minimal + "[ports]\nranges = [\"40000-40100\"]\n",
		"bad var range":    minimal + "[ports.vars]\nPORT = \"10-20\"\n",
		"dup dsn":          minimal + "[[dsn]]\nvar = \"A\"\n[[dsn]]\nvar = \"A\"\n",
		"bad dsn role":     minimal + "[[dsn]]\nvar = \"A\"\nrole = \"Bad-Role\"\n",
		"bad dsn format":   minimal + "[[dsn]]\nvar = \"A\"\nformat = \"yaml\"\n",
		"abs env file":     minimal + "[env]\nfiles = [\"/etc/passwd\"]\n",
		"escape env file":  minimal + "[env]\nfiles = [\"../x/.env\"]\n",
		"bad template":     minimal + "[env.vars]\nX = \"{{.App\"\n",
		"bad duplicate":    minimal + "[env]\non_duplicate = \"ignore\"\n",
		"bad timeout":      minimal + "timeout = \"forever\"\n",
		"bad input":        strings.Replace(minimal, "[template]\n", "[template]\ninputs = [\"../secrets\"]\n", 1),
		"bad on_up_fail":   minimal + "[hooks.claude]\non_up_failure = \"retry\"\n",
		"bad keep":         strings.Replace(minimal, "[template]\n", "[template]\nkeep = 0\n", 1),
		"bad extension":    strings.Replace(minimal, "[template]\n", "[template]\nextensions = [\"x;drop\"]\n", 1),
		"bad reserved":     minimal + "[ports]\nreserved = [70000]\n",
		"bad overlay":      minimal, // overlay written below
		"bad vars env key": minimal + "[env.vars]\n\"1X\" = \"a\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := write(t, dir, FileName, body)
			if name == "bad overlay" {
				write(t, dir, ".worktree-pg.local.toml", "nope = true\n")
			}
			c, err := Load(p)
			if err == nil && c != nil {
				err = c.ValidateWith(eph)
			}
			if errs.CodeOf(err) != errs.CodeConfigInvalid || errs.ExitCode(err) != 3 {
				t.Fatalf("want CONFIG_INVALID, got %v", err)
			}
		})
	}
}

func TestFindAndAdminDSN(t *testing.T) {
	dir := t.TempDir()
	if _, err := Find(dir); errs.CodeOf(err) != errs.CodeConfigNotFound {
		t.Fatalf("Find on empty dir: %v", err)
	}
	write(t, dir, FileName, minimal)
	c, err := Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"WORKTREE_PG_ADMIN_DSN": " postgres://a@h/db "}
	if v, err := c.ResolveAdminDSN(func(k string) string { return env[k] }); err != nil || v != "postgres://a@h/db" {
		t.Fatalf("env dsn: %q %v", v, err)
	}
	none := func(string) string { return "" }
	if _, err := c.ResolveAdminDSN(none); errs.CodeOf(err) != errs.CodeAdminDSNMissing || errs.ExitCode(err) != 4 {
		t.Fatalf("missing dsn: %v", err)
	}
	f := write(t, dir, "dsn", "postgres://file@h/db\n")
	_ = os.Chmod(f, 0o644) // independent of the process umask
	c.Cluster.AdminDSNFile = f
	if _, err := c.ResolveAdminDSN(none); errs.CodeOf(err) != errs.CodeAdminDSNMissing {
		t.Fatalf("0644 dsn file accepted: %v", err)
	}
	_ = os.Chmod(f, 0o600)
	if v, err := c.ResolveAdminDSN(none); err != nil || v != "postgres://file@h/db" {
		t.Fatalf("file dsn: %q %v", v, err)
	}
	c.Cluster.AdminDSNFile = filepath.Join(dir, "missing")
	if _, err := c.ResolveAdminDSN(none); err == nil {
		t.Fatal("missing file accepted")
	}
	if b, _ := (Duration{time.Second}).MarshalText(); string(b) != "1s" {
		t.Fatal("MarshalText")
	}
}
