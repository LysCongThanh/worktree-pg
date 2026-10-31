package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// Claude Code hook commands installed into settings.json. They rely on
// worktree-pg being on PATH, like every other hook command.
const (
	ClaudeCreateCmd = "worktree-pg claude-hook create"
	ClaudeRemoveCmd = "worktree-pg claude-hook remove"
	claudeTimeout   = 600
)

// claudeEvents maps each Claude Code hook event to our command.
var claudeEvents = []struct{ Event, Command string }{
	{"WorktreeCreate", ClaudeCreateCmd},
	{"WorktreeRemove", ClaudeRemoveCmd},
}

// ClaudeSettingsPath returns the settings file of a scope: local
// (<root>/.claude/settings.local.json, the default), project
// (<root>/.claude/settings.json) or user (<home>/.claude/settings.json).
func ClaudeSettingsPath(scope, root, home string) (string, error) {
	switch scope {
	case "", "local":
		return filepath.Join(root, ".claude", "settings.local.json"), nil
	case "project":
		return filepath.Join(root, ".claude", "settings.json"), nil
	case "user":
		return filepath.Join(home, ".claude", "settings.json"), nil
	}
	return "", errs.New(errs.Config, errs.CodeConfigInvalid, "unknown scope %q (local | project | user)", scope)
}

// EditClaudeSettings adds (install) or removes our hook commands in the settings
// JSON at path. Other keys and hooks are preserved (keys are re-sorted by the JSON
// encoder). It returns the new document and whether it differs from the old one.
// Nothing is written: see WriteWithBackup.
func EditClaudeSettings(path string, install bool) (out []byte, changed bool, err error) {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, false, errs.Wrap(errs.Internal, errs.CodeInternal, err, "read %s", path)
	}
	doc := map[string]any{}
	if len(bytes.TrimSpace(old)) > 0 {
		if err := json.Unmarshal(old, &doc); err != nil {
			return nil, false, errs.Wrap(errs.Config, errs.CodeConfigInvalid, err, "%s is not a JSON object", path)
		}
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, ev := range claudeEvents {
		groups, _ := hooks[ev.Event].([]any)
		groups = withoutCommand(groups, ev.Command)
		if install {
			groups = append(groups, map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": ev.Command, "timeout": claudeTimeout,
			}}})
		}
		if len(groups) == 0 {
			delete(hooks, ev.Event)
		} else {
			hooks[ev.Event] = groups
		}
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooks
	}
	out, err = json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, false, errs.Wrap(errs.Internal, errs.CodeInternal, err, "encode settings")
	}
	out = append(out, '\n')
	if len(bytes.TrimSpace(old)) == 0 && !install {
		return out, false, nil // nothing to uninstall: never create an empty file
	}
	return out, !sameJSON(old, out), nil
}

// withoutCommand drops every hook entry running cmd and any group left empty, so
// install is idempotent and uninstall leaves foreign hooks alone.
func withoutCommand(groups []any, cmd string) []any {
	var keep []any
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			keep = append(keep, g)
			continue
		}
		list, _ := gm["hooks"].([]any)
		var rest []any
		for _, h := range list {
			if hm, ok := h.(map[string]any); ok && hm["command"] == cmd {
				continue
			}
			rest = append(rest, h)
		}
		if len(rest) == 0 && len(list) > 0 {
			continue
		}
		if len(list) > 0 {
			gm["hooks"] = rest
		}
		keep = append(keep, gm)
	}
	return keep
}

// sameJSON compares two documents semantically (formatting differences ignored).
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}

// WriteWithBackup writes data to path atomically, first copying an existing file
// to path+".bak". The parent directory is created (0755) when missing.
func WriteWithBackup(path string, data []byte, mode fs.FileMode) error {
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", old, 0o600); err != nil {
			return errs.Wrap(errs.Internal, errs.CodeInternal, err, "write backup")
		}
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errs.Wrap(errs.Internal, errs.CodeInternal, err, "create %s", filepath.Dir(path))
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wtpg-*")
	if err != nil {
		return errs.Wrap(errs.Internal, errs.CodeInternal, err, "write %s", path)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return errs.Wrap(errs.Internal, errs.CodeInternal, err, "write %s", path)
	}
	return nil
}
