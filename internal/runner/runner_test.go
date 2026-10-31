package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

func TestRunSuccessEnvAndLog(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WORKTREE_PG_ADMIN_DSN", "postgres://admin:topsecret@h/db")
	t.Setenv("KEEP_ME", "1")
	log := LogPath(dir, "k1", "migrate", time.Now())
	var echo bytes.Buffer
	err := Run(context.Background(), Cmd{
		Label: "migrate_cmd", Code: errs.CodeMigrateFailed, Dir: dir,
		Script:  `echo "url=$DATABASE_URL admin=${WORKTREE_PG_ADMIN_DSN:-none} keep=$KEEP_ME pwd=$(pwd)"`,
		Env:     []string{"DATABASE_URL=postgres://wt:pw@h/wt", "KEEP_ME=2"},
		Strip:   []string{"WORKTREE_PG_ADMIN_DSN"},
		LogPath: log, Echo: &echo,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	real, _ := filepath.EvalSymlinks(dir)
	want := fmt.Sprintf("url=postgres://wt:pw@h/wt admin=none keep=2 pwd=%s", real)
	if !strings.Contains(string(b), want) || !strings.Contains(echo.String(), want) {
		t.Fatalf("log %q / echo %q missing %q", b, echo.String(), want)
	}
	if strings.Contains(string(b), "topsecret") {
		t.Fatal("admin DSN leaked to user command")
	}
	if st, _ := os.Stat(log); st.Mode().Perm() != 0o600 {
		t.Fatalf("log perm %o", st.Mode().Perm())
	}
}

func TestRunFailureTimeoutCancel(t *testing.T) {
	dir := t.TempDir()
	err := Run(context.Background(), Cmd{Label: "seed_cmd", Code: errs.CodeSeedFailed, Script: "echo nope >&2; exit 3", LogPath: filepath.Join(dir, "l.log")})
	e := errs.As(err)
	if e.Code != errs.CodeSeedFailed || errs.ExitCode(err) != 5 || e.LogPath == "" || !strings.Contains(e.Msg, "exit code 3") {
		t.Fatalf("failure: %+v", e)
	}
	start := time.Now()
	err = Run(context.Background(), Cmd{Label: "migrate_cmd", Code: errs.CodeMigrateFailed, Script: "sleep 30 & sleep 30", Timeout: 200 * time.Millisecond})
	if errs.CodeOf(err) != errs.CodeMigrateFailed || time.Since(start) > 10*time.Second {
		t.Fatalf("timeout: %v after %s", err, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if err := Run(ctx, Cmd{Label: "x", Code: "X", Script: "sleep 30"}); errs.ExitCode(err) != 130 {
		t.Fatalf("cancel: %v", err)
	}
	if err := Run(context.Background(), Cmd{Label: "x", Code: "X", Script: "true", Dir: filepath.Join(dir, "missing")}); errs.ExitCode(err) != 5 {
		t.Fatalf("bad dir: %v", err)
	}
}

func TestMergeEnv(t *testing.T) {
	got := MergeEnv([]string{"A=1", "B=2", "SECRET=x"}, []string{"B=3", "C=4"}, []string{"SECRET"})
	if strings.Join(got, ",") != "A=1,B=3,C=4" {
		t.Fatal(got)
	}
}

func TestPruneLogs(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o700)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 25; i++ {
		p := LogPath(dir, "k", fmt.Sprint(i), base.Add(time.Duration(i)*time.Second))
		_ = os.WriteFile(p, nil, 0o600)
		_ = os.Chtimes(p, base.Add(time.Duration(i)*time.Second), base.Add(time.Duration(i)*time.Second))
	}
	if err := PruneLogs(dir, KeepLogs); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "logs", "*.log"))
	if len(left) != KeepLogs {
		t.Fatalf("kept %d", len(left))
	}
	for _, f := range left {
		if strings.HasSuffix(f, "-0.log") || strings.HasSuffix(f, "-4.log") {
			t.Fatalf("oldest log kept: %s", f)
		}
	}
	if err := PruneLogs(t.TempDir(), 1); err != nil {
		t.Fatal(err)
	}
}
