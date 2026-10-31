package cli

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/backend"
	"github.com/LysCongThanh/worktree-pg/internal/doctor"
	"github.com/LysCongThanh/worktree-pg/internal/engine"
	"github.com/LysCongThanh/worktree-pg/internal/envfile"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

var update = flag.Bool("update", false, "rewrite testdata/golden/*.json")

// goldenCases render fixed results through the real output path (--json).
// They pin the wire format consumed by agents and scripts (spec §6).
func goldenCases() map[string]func(a *app) int {
	up := &engine.UpResult{
		Worktree: engine.WorktreeRef{Path: "/src/shop-feat", Key: "aaaabbbbccccdddd", Slug: "shop_feat", Branch: "feat"},
		Cluster:  engine.ClusterRef{ID: "ext-5432", Kind: "external", Host: "127.0.0.1", Port: 5432, PGMajor: 17},
		Database: "shop_wt_shop_feat_aaaabbbb", Role: "shop_wt_shop_feat_01234567",
		Template: engine.TemplateRef{Name: "shop_tpl_89abcdef", Fingerprint: "89abcdef00", Built: true},
		Ports:    map[string]int{"PORT": 5601, "ADMIN_PORT": 5602},
		EnvFiles: []envfile.Result{{Path: "/src/shop-feat/.env", Changed: true, Created: true, DuplicateKeys: []string{}}},
		Actions:  []string{"create_role", "create_database", "write_env"}, DurationMS: 812,
		Warnings: []engine.Warning{{Code: "PORT_BUSY", Message: "PORT 5601 is in use"}},
	}
	return map[string]func(a *app) int{
		"version": func(a *app) int { return a.run(context.Background(), []string{"--json", "version"}) },
		"usage_unknown_command": func(a *app) int {
			return a.run(context.Background(), []string{"--json", "bogus"})
		},
		"up_success": func(a *app) int { return a.finish("up", up, up.Warnings, nil, nil) },
		"up_failure_migrate": func(a *app) int {
			err := errs.New(errs.UserCmd, errs.CodeMigrateFailed, "migrate_cmd failed with exit code 1").
				WithHint("see the log for the command output").WithLog("/state/logs/k-1-migrate_cmd.log")
			return a.finish("up", (*engine.UpResult)(nil), nil, err, nil)
		},
		"down_success": func(a *app) int {
			res := &engine.DownResult{Worktree: up.Worktree, Database: up.Database, Role: up.Role,
				EnvFiles: up.EnvFiles, Actions: []string{"drop_database", "drop_role", "strip_env"}, Orphans: []string{}, DurationMS: 90}
			return a.finish("down", res, nil, nil, nil)
		},
		"status_list": func(a *app) int {
			list := []engine.WorktreeStatus{{Worktree: up.Worktree, Status: "ready", Cluster: up.Cluster, Database: up.Database,
				Role: up.Role, Ports: up.Ports, EnvFiles: []string{".env"}, Template: engine.TemplateRef{Name: up.Template.Name, Stale: true},
				PathExists: true, Health: &backend.Health{DBExists: true, RoleExists: true, MarkerOK: true, Sessions: 2}}}
			return a.finish("status", list, nil, nil, nil)
		},
		"gc_dry_run": func(a *app) int {
			rep := &engine.GCReport{DryRun: true, Items: []engine.GCItem{{Kind: "worktree", Name: up.Database, Key: up.Worktree.Key,
				Path: "/src/gone", Reason: "prunable: gitdir file points to non-existent location", Action: "would_drop"}}}
			return a.finish("gc", rep, nil, nil, nil)
		},
		"env_json": func(a *app) int {
			res := envResult{Path: up.Worktree.Path, Vars: []envfile.Var{{Key: "DATABASE_URL", Value: "postgres://r:pw@127.0.0.1:5432/d", Secret: true}, {Key: "PORT", Value: "5601"}}}
			return a.finish("env", res, nil, nil, nil)
		},
		"not_provisioned": func(a *app) int {
			return a.finish("env", nil, nil, errs.New(errs.NotFound, errs.CodeNotProvisioned, "/src/x is not provisioned").WithHint("run `worktree-pg up`"), nil)
		},
		"doctor_failed": func(a *app) int {
			rep := doctor.Report{Checks: []doctor.Check{{Name: "git", Status: doctor.OK, Detail: "git version 2.55.0"},
				{Name: "admin dsn", Status: doctor.Fail, Detail: "admin DSN not set", Hint: "export WORKTREE_PG_ADMIN_DSN=..."}}}
			return a.finishKeep("doctor", rep, rep.Err(), nil)
		},
		"hook_refused_husky": func(a *app) int {
			r := hookResult{Target: "git", Action: "install", Path: "/src/shop/.husky/post-checkout", Content: "# snippet\n"}
			err := errs.New(errs.Conflict, "HOOKS_PATH_MANAGED", "core.hooksPath=.husky is inside the work tree")
			return a.finishKeep("hook install git", r, err, nil)
		},
	}
}

func TestGoldenJSON(t *testing.T) {
	for name, fn := range goldenCases() {
		t.Run(name, func(t *testing.T) {
			a, out, _ := testApp(t, "", nil)
			a.g.JSON = true
			code := fn(a)
			path := filepath.Join("testdata", "golden", name+".json")
			if *update {
				_ = os.MkdirAll(filepath.Dir(path), 0o755)
				if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/cli -run TestGoldenJSON -update)", err)
			}
			if out.String() != string(want) {
				t.Fatalf("exit %d, JSON differs from %s:\n%s", code, path, out)
			}
			if strings.Count(out.String(), `"schema": "worktree-pg/v1"`) != 1 {
				t.Fatal("must print exactly one envelope")
			}
			if name != "env_json" && strings.Contains(out.String(), ":pw@") {
				t.Fatal("password outside env --format json (invariant S6)")
			}
		})
	}
}

// TestHumanOutput smoke-tests the text renderers.
func TestHumanOutput(t *testing.T) {
	var b strings.Builder
	printUp(&b, &engine.UpResult{Worktree: engine.WorktreeRef{Path: "/w"}, Database: "d", Ports: map[string]int{"B": 2, "A": 1},
		Template: engine.TemplateRef{Name: "t", Built: true, Stale: true},
		EnvFiles: []envfile.Result{{Path: ".env", Created: true, DuplicateKeys: []string{"PORT"}}}})
	printDown(&b, &engine.DownResult{Worktree: engine.WorktreeRef{Path: "/w"}, Database: "d", Orphans: []string{"role r"}}, true)
	printStatus(&b, nil)
	printStatus(&b, []engine.WorktreeStatus{{Status: "failed", LastError: "boom", Health: &backend.Health{}}})
	printGC(&b, &engine.GCReport{DryRun: true, Items: []engine.GCItem{{Action: "would_drop", Kind: "worktree", Key: "k"}}})
	printGC(&b, &engine.GCReport{})
	printDoctor(&b, doctor.Report{Checks: []doctor.Check{{Name: "x", Status: doctor.Warn, Detail: "d", Hint: "h"}}})
	s := b.String()
	for _, want := range []string{"ready (no changes)", "A=1 B=2", "(built now)", "stale", "created", "also set outside the block: PORT",
		"orphan    role r", "worktree  removed", "no provisioned worktrees", "database missing", "last error: boom",
		"dry run: re-run with --yes", "nothing to collect", "[WARN]", "hint: h"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}
