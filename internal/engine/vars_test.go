package engine

import (
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
)

func TestRenderVarsOrderAndTemplates(t *testing.T) {
	cfg := config.Default()
	cfg.DSN = []config.DSN{{Var: "DATABASE_URL", Role: pg.OwnerRef, Format: "url", Params: "sslmode=disable"}}
	cfg.Ports.Vars = map[string]string{"PORT": "", "ADMIN_PORT": ""}
	cfg.Env.Vars = map[string]string{
		"COMPOSE_PROJECT_NAME": "{{.App}}_{{.Slug}}",
		"GOOSE_DBSTRING":       `{{DSN "DATABASE_URL"}}`,
		"APP_URL":              `http://127.0.0.1:{{Port "PORT"}}`,
	}
	cred := pg.Cred{Host: "127.0.0.1", Port: 5432, User: "shop_wt_a", Password: "pw", Database: "shop_wt_a"}
	vars, err := renderVars(cfg, varData{App: "shop", Slug: "a"}, map[string]int{"PORT": 5601, "ADMIN_PORT": 5602}, cred, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"DATABASE_URL", "ADMIN_PORT", "PORT", "APP_URL", "COMPOSE_PROJECT_NAME", "GOOSE_DBSTRING"}
	wantVals := []string{"postgres://shop_wt_a:pw@127.0.0.1:5432/shop_wt_a?sslmode=disable", "5602", "5601",
		"http://127.0.0.1:5601", "shop_a", "postgres://shop_wt_a:pw@127.0.0.1:5432/shop_wt_a?sslmode=disable"}
	for i, v := range vars {
		if v.Key != wantKeys[i] || v.Value != wantVals[i] {
			t.Fatalf("var %d = %+v, want %s=%s", i, v, wantKeys[i], wantVals[i])
		}
	}
	if !vars[0].Secret || !vars[5].Secret || vars[3].Secret {
		t.Fatal("secret flags: values embedding the password must be secret")
	}
	cfg.Env.Vars = map[string]string{"BAD": `{{Port "NOPE"}}`}
	if _, err := renderVars(cfg, varData{}, nil, cred, func(string) string { return "" }); errs.CodeOf(err) != errs.CodeConfigInvalid {
		t.Fatalf("unknown Port: %v", err)
	}
}
