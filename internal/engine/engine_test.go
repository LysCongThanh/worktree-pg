package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

func TestOpenAndStubs(t *testing.T) {
	dir := t.TempDir()
	e, err := Open(Options{StateDir: dir, Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if e.Store == nil || e.Secret == nil || e.Opts.Getenv == nil || e.Opts.Now == nil {
		t.Fatal("defaults not applied")
	}
	ctx := context.Background()
	_, e1 := e.Up(ctx, UpRequest{})
	_, e3 := e.Status(ctx, StatusRequest{})
	_, e4 := e.GC(ctx, GCRequest{})
	_, e5 := e.Env(ctx, dir)
	for _, err := range []error{e1, e3, e4, e5} { // dir is not a git work tree
		if errs.CodeOf(err) != errs.CodeNotGitRepo {
			t.Fatalf("outside a repository: %v", err)
		}
	}
	if res, err := e.Down(ctx, DownRequest{}); err != nil || len(res.Actions) != 0 {
		t.Fatalf("down of an unregistered dir must be a no-op: %v %v", res, err)
	}
	t.Setenv("WORKTREE_PG_STATE_DIR", t.TempDir())
	if _, err := Open(Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestEnvelopeJSON(t *testing.T) {
	ok, _ := json.Marshal(Success("up", &UpResult{Ports: map[string]int{"PORT": 5600}}, nil))
	for _, want := range []string{`"schema":"worktree-pg/v1"`, `"ok":true`, `"warnings":[]`, `"PORT":5600`, `"env_files":null`} {
		if !strings.Contains(string(ok), want) {
			t.Errorf("success JSON %s missing %s", ok, want)
		}
	}
	if strings.Contains(string(ok), "Warnings") || strings.Contains(string(ok), `"error"`) {
		t.Errorf("unexpected member in %s", ok)
	}
	bad, _ := json.Marshal(Failure("up", errs.New(errs.Busy, errs.CodeLeaseHeld, "busy").WithHint("wait"), nil))
	for _, want := range []string{`"ok":false`, `"code":"LEASE_HELD"`, `"exit":6`, `"hint":"wait"`} {
		if !strings.Contains(string(bad), want) {
			t.Errorf("failure JSON %s missing %s", bad, want)
		}
	}
}
