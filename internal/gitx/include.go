package gitx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// IncludeFile lists gitignore-style patterns of ignored files to copy into new worktrees.
const IncludeFile = ".worktreeinclude"

// CopyWorktreeInclude re-implements Claude Code's .worktreeinclude behaviour for
// hook-created worktrees: copy files from src to dst that match .worktreeinclude
// AND are git-ignored in src. Existing files in dst are never overwritten and
// symlinks are skipped. Returns the copied relative paths.
func CopyWorktreeInclude(ctx context.Context, src, dst string) ([]string, error) {
	inc := filepath.Join(src, IncludeFile)
	if _, err := os.Stat(inc); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	// untracked files matching the include patterns (git does the pattern matching)
	matched, err := gitRaw(ctx, src, nil, "ls-files", "--others", "--ignored", "-z", "--exclude-from="+inc)
	if err != nil {
		return nil, err
	}
	cands := splitZ(matched)
	if len(cands) == 0 {
		return nil, nil
	}
	ignored, err := checkIgnored(ctx, src, cands)
	if err != nil {
		return nil, err
	}
	var copied []string
	for _, rel := range cands {
		if !ignored[rel] || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue
		}
		ok, err := copyNoOverwrite(filepath.Join(src, rel), filepath.Join(dst, rel))
		if err != nil {
			return copied, err
		}
		if ok {
			copied = append(copied, rel)
		}
	}
	return copied, nil
}

// checkIgnored returns the subset of paths ignored by the repo's standard rules.
func checkIgnored(ctx context.Context, dir string, paths []string) (map[string]bool, error) {
	cmd := commandIn(ctx, dir, "check-ignore", "--stdin", "-z")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	var ee interface{ ExitCode() int }
	if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) { // 1 = nothing ignored
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range splitZ(out) {
		set[p] = true
	}
	return set, nil
}

// copyNoOverwrite copies a regular file preserving its mode; it skips symlinks,
// non-regular files and existing destinations.
func copyNoOverwrite(from, to string) (bool, error) {
	st, err := os.Lstat(from)
	if err != nil || !st.Mode().IsRegular() {
		return false, nil
	}
	if _, err := os.Lstat(to); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return false, err
	}
	in, err := os.Open(from)
	if err != nil {
		return false, err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return false, err
	}
	if err = out.Chmod(st.Mode().Perm()); err == nil { // exact mode, independent of umask
		_, err = io.Copy(out, in)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err == nil, err
}

func splitZ(b []byte) []string {
	var out []string
	for _, f := range bytes.Split(b, []byte{0}) {
		if len(f) > 0 {
			out = append(out, string(f))
		}
	}
	return out
}
