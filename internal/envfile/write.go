package envfile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// Result describes what happened to one env file (JSON: env_files[]).
type Result struct {
	Path          string   `json:"path"`
	Changed       bool     `json:"changed"`
	Created       bool     `json:"created"`
	DuplicateKeys []string `json:"duplicate_keys"`
}

// Write splices vars into root/rel atomically with mode 0600. It refuses symlinked
// targets, targets resolving outside root and missing parent directories
// (ENV_TARGET_UNSAFE, exit 2). Unchanged content is not rewritten (idempotent, S11).
func Write(root, rel string, vars []Var) (Result, error) {
	target, err := safeTarget(root, rel)
	if err != nil {
		return Result{}, err
	}
	res := Result{Path: target, DuplicateKeys: []string{}}
	old, exists, err := readRegular(target)
	if err != nil {
		return res, err
	}
	out, dups, err := Splice(old, vars)
	if err != nil {
		return res, err
	}
	res.DuplicateKeys = dups
	if exists && bytes.Equal(old, out) && perm(target) == 0o600 {
		return res, nil
	}
	if err := atomicWrite(target, out); err != nil {
		return res, err
	}
	res.Changed, res.Created = true, !exists
	return res, nil
}

// Remove strips the managed block from root/rel. When the file was created by
// worktree-pg (createdFile) and nothing else remains, the file is deleted.
func Remove(root, rel string, createdFile bool) (changed bool, err error) {
	target, err := safeTarget(root, rel)
	if err != nil {
		return false, err
	}
	old, exists, err := readRegular(target)
	if err != nil || !exists {
		return false, err
	}
	out, found, err := Strip(old)
	if err != nil || !found {
		return false, err
	}
	if createdFile && strings.TrimSpace(string(out)) == "" {
		return true, os.Remove(target)
	}
	return true, atomicWrite(target, out)
}

// safeTarget resolves root/rel and verifies it stays inside root and is not a symlink.
func safeTarget(root, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(rel) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", unsafe("%s escapes the worktree root", rel)
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errs.Wrap(errs.Conflict, errs.CodeEnvTargetUnsafe, err, "resolve worktree root")
	}
	target := filepath.Join(rootReal, clean)
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return "", unsafe("parent directory of %s does not exist", rel)
	}
	if parentReal != rootReal && !strings.HasPrefix(parentReal, rootReal+string(filepath.Separator)) {
		return "", unsafe("%s resolves outside the worktree root", rel)
	}
	target = filepath.Join(parentReal, filepath.Base(target))
	if st, err := os.Lstat(target); err == nil && !st.Mode().IsRegular() {
		return "", unsafe("%s is a symlink or not a regular file", rel)
	}
	return target, nil
}

func unsafe(format string, a ...any) error {
	return errs.New(errs.Conflict, errs.CodeEnvTargetUnsafe, format, a...).
		WithHint("worktree-pg only writes regular files inside the worktree; fix [env].files")
}

// readRegular reads a file, reporting existence separately from errors.
func readRegular(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errs.Wrap(errs.Internal, errs.CodeInternal, err, "read %s", path)
	}
	return b, true, nil
}

func perm(path string) fs.FileMode {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Mode().Perm()
}

// atomicWrite writes data to a 0600 temp file in the same directory, fsyncs it,
// renames it over path and fsyncs the directory.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".wtpg-env-*")
	if err != nil {
		return errs.Wrap(errs.Internal, errs.CodeInternal, err, "create temp env file")
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err = f.Chmod(0o600); err == nil {
		if _, err = f.Write(data); err == nil {
			err = f.Sync()
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		return errs.Wrap(errs.Internal, errs.CodeInternal, err, "write %s", path)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
