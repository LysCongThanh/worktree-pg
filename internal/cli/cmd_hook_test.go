package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookInstallGit(t *testing.T) {
	root := newRepo(t, "")
	hook := filepath.Join(root, ".git", "hooks", "post-checkout")
	if code, out, e := cli(t, "hook", "install", "git", "--path", root); code != 0 || !strings.Contains(out, "installed") {
		t.Fatalf("%d %q %q", code, out, e)
	}
	st, err := os.Stat(hook)
	if err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("hook not executable: %v", err)
	}
	if code, out, _ := cli(t, "hook", "install", "git", "--path", root); code != 0 || !strings.Contains(out, "already up to date") {
		t.Fatalf("not idempotent: %q", out)
	}
	if code, out, _ := cli(t, "--json", "hook", "install", "git", "--print", "--path", root); code != 0 || !strings.Contains(out, `"content": "#!/bin/sh`) {
		t.Fatalf("--print: %q", out)
	}
	if code, _, _ := cli(t, "hook", "uninstall", "git", "--path", root); code != 0 {
		t.Fatal("uninstall")
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatal("hook created by us should be gone after uninstall")
	}
	// foreign hook: block appended, foreign body kept, .bak written
	_ = os.WriteFile(hook, []byte("#!/bin/sh\necho mine\n"), 0o755)
	cli(t, "hook", "install", "git", "--path", root)
	b, _ := os.ReadFile(hook)
	bak, _ := os.ReadFile(hook + ".bak")
	if !strings.HasPrefix(string(b), "#!/bin/sh\necho mine\n# >>> worktree-pg") || string(bak) != "#!/bin/sh\necho mine\n" {
		t.Fatalf("hook %q bak %q", b, bak)
	}
}

func TestHookInstallGitRefusesHusky(t *testing.T) {
	root := newRepo(t, "")
	_ = os.Mkdir(filepath.Join(root, ".husky"), 0o755)
	_ = exec.Command("git", "-C", root, "config", "core.hooksPath", ".husky").Run()
	code, out, errOut := cli(t, "hook", "install", "git", "--path", root)
	if code != 2 || !strings.Contains(out, "worktree-pg git-hook post-checkout") || !strings.Contains(errOut, "HOOKS_PATH_MANAGED") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, ".husky", "post-checkout")); !os.IsNotExist(err) {
		t.Fatal("husky hook written")
	}
}

func TestHookInstallClaude(t *testing.T) {
	root := newRepo(t, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(root, ".claude", "settings.local.json")
	if code, _, e := cli(t, "hook", "install", "claude", "--path", root); code != 0 {
		t.Fatal(e)
	}
	if b, _ := os.ReadFile(local); !strings.Contains(string(b), "worktree-pg claude-hook create") {
		t.Fatalf("local settings: %s", b)
	}
	if code, _, _ := cli(t, "hook", "install", "claude", "--scope", "user", "--path", root); code != 0 {
		t.Fatal("user scope")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := cli(t, "hook", "install", "claude", "--scope", "galaxy", "--path", root); code != 3 {
		t.Fatal("bad scope accepted")
	}
	if code, _, _ := cli(t, "hook", "uninstall", "claude", "--path", root); code != 0 {
		t.Fatal("uninstall")
	}
	if b, _ := os.ReadFile(local); strings.Contains(string(b), "claude-hook") {
		t.Fatalf("still installed: %s", b)
	}
}

// TestInvariant_S9_DownRemoveWorktreeRefusesMain: the main worktree is refused
// before anything is torn down.
func TestInvariant_S9_DownRemoveWorktreeRefusesMain(t *testing.T) {
	root := newRepo(t, "version = 1\nname = \"shop\"\n[template]\nmigrate_cmd = \"true\"\n")
	code, _, errOut := cli(t, "down", "--remove-worktree", "--path", root)
	if code != 2 || !strings.Contains(errOut, "NOT_REGISTERED_WORKTREE") {
		t.Fatalf("%d %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, ".worktree-pg.toml")); err != nil {
		t.Fatal("main worktree touched")
	}
}

// TestClaudeHookCreateViaCLI pipes a payload through the real CLI and engine (no
// admin DSN): provisioning fails, stdout stays empty and the worktree is rolled back.
func TestClaudeHookCreateViaCLI(t *testing.T) {
	root := newRepo(t, "version = 1\nname = \"shop\"\n[template]\nmigrate_cmd = \"true\"\n")
	a, out, errb := testApp(t, `{"hook_event_name":"WorktreeCreate","name":"x1"}`, map[string]string{"CLAUDE_PROJECT_DIR": root})
	a.g.Path = root
	code := a.run(context.Background(), []string{"--json", "claude-hook", "create"})
	if code == 0 || out.Len() != 0 || !strings.Contains(errb.String(), "rolling back") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errb)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "worktrees", "x1")); !os.IsNotExist(err) {
		t.Fatal("not rolled back")
	}
}
