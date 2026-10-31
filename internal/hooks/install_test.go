package hooks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

func TestEditClaudeSettings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.local.json")
	foreign := `{"permissions":{"allow":["Bash(ls)"]},"hooks":{"WorktreeCreate":[{"hooks":[{"type":"command","command":"other"}]}],"Stop":[]}}`
	write(t, p, foreign)
	out, changed, err := EditClaudeSettings(p, true)
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	s := string(out)
	for _, want := range []string{`"Bash(ls)"`, `"other"`, `"` + ClaudeCreateCmd + `"`, `"` + ClaudeRemoveCmd + `"`, `"timeout": 600`, `"Stop": []`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if err := WriteWithBackup(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p + ".bak"); string(b) != foreign {
		t.Fatal("backup is not the original")
	}
	again, changed, _ := EditClaudeSettings(p, true)
	if changed || !bytes.Equal(again, out) || strings.Count(string(again), ClaudeCreateCmd) != 1 {
		t.Fatal("install is not idempotent")
	}
	un, changed, _ := EditClaudeSettings(p, false)
	if !changed || strings.Contains(string(un), "claude-hook") || !strings.Contains(string(un), `"other"`) {
		t.Fatalf("uninstall: %s", un)
	}
	if _, changed, _ := EditClaudeSettings(filepath.Join(dir, "none.json"), false); changed {
		t.Fatal("uninstall must not create a file")
	}
	write(t, p, "[1]")
	if _, _, err := EditClaudeSettings(p, true); errs.ExitCode(err) != 3 {
		t.Fatalf("non-object settings: %v", err)
	}
	if _, err := ClaudeSettingsPath("bogus", dir, dir); err == nil {
		t.Fatal("bogus scope accepted")
	}
}

func TestEditGitHook(t *testing.T) {
	out, changed, _ := EditGitHook(nil, true)
	if !changed || !strings.HasPrefix(string(out), "#!/bin/sh\n"+GitBlockBegin) {
		t.Fatalf("fresh hook: %q", out)
	}
	if _, changed, _ := EditGitHook(out, true); changed {
		t.Fatal("not idempotent")
	}
	if _, changed, remove := EditGitHook(out, false); !changed || !remove {
		t.Fatal("uninstall of our own hook should delete it")
	}
	foreign := []byte("#!/bin/bash\necho lefthook\n")
	out, changed, _ = EditGitHook(foreign, true)
	if !changed || !strings.HasPrefix(string(out), string(foreign)) || !strings.Contains(string(out), GitBlockEnd) {
		t.Fatalf("foreign append: %q", out)
	}
	back, changed, remove := EditGitHook(out, false)
	if !changed || remove || !bytes.Equal(back, foreign) {
		t.Fatalf("foreign uninstall: %q remove=%v", back, remove)
	}
	if !ExitsEarly([]byte("#!/bin/sh\nexit 0\n")) || ExitsEarly(foreign) {
		t.Fatal("ExitsEarly")
	}
	// a stale block in the middle is replaced in place
	stale := "#!/bin/sh\n" + GitBlockBegin + "\nold\n" + GitBlockEnd + "\necho after\n"
	out, _, _ = EditGitHook([]byte(stale), true)
	if !strings.HasSuffix(string(out), "echo after\n") || strings.Contains(string(out), "old\n") {
		t.Fatalf("replace in place: %q", out)
	}
}

func TestGitHookPath(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t, "")
	p, err := GitHookPath(ctx, root)
	if err != nil || p != filepath.Join(root, ".git", "hooks", "post-checkout") {
		t.Fatalf("%q %v", p, err)
	}
	wt := filepath.Join(root, ".claude", "worktrees", "b")
	git(t, root, "worktree", "add", "-q", "-b", "b", wt)
	if lp, _ := GitHookPath(ctx, wt); lp != p {
		t.Fatalf("linked worktree hook path %q, want shared %q", lp, p)
	}
	git(t, root, "config", "core.hooksPath", ".husky")
	_ = os.Mkdir(filepath.Join(root, ".husky"), 0o755)
	if _, err := GitHookPath(ctx, root); errs.CodeOf(err) != CodeHooksPathManaged || errs.ExitCode(err) != 2 {
		t.Fatalf("husky: %v", err)
	}
}
