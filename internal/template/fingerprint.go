// Package template builds, refreshes and garbage-collects the migrated template
// database that every worktree clone is created from.
//
// This file computes the template fingerprint: a hash of everything that changes
// the template's contents, so clones are re-migrated only when inputs change.
package template

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
)

// FPVersion prefixes the canonical lines; bump it when the line format changes.
const FPVersion = "fp/1"

// Fingerprint returns hex(sha256) of the canonical template description. image is
// the docker image for managed clusters or "" for external ones. File contents
// are hashed (never mtimes), so the value is stable across checkouts.
func Fingerprint(root string, c *config.Config, srv pg.ServerInfo, image string) (string, error) {
	lines, err := Lines(root, c, srv, image)
	if err != nil {
		return "", err
	}
	return hashLines(lines), nil
}

// Lines returns the sorted canonical lines behind Fingerprint (shown by `template status -v`).
func Lines(root string, c *config.Config, srv pg.ServerInfo, image string) ([]string, error) {
	cluster := "external"
	if image != "" {
		cluster = "image " + image
	}
	exts := append([]string(nil), c.Template.Extensions...)
	sort.Strings(exts)
	setup, err := SetupFingerprint(root, c)
	if err != nil {
		return nil, err
	}
	lines := []string{
		fmt.Sprintf("pg_major %d", srv.Major),
		"cluster " + cluster,
		"roles " + c.Database.Roles,
		"extensions " + strings.Join(exts, ","),
		"cmd prepare " + c.Template.PrepareCmd,
		"cmd migrate " + c.Template.MigrateCmd,
		"cmd seed " + c.Template.SeedCmd,
		"setup " + setup,
	}
	files, err := hashInputs(root, c.Template.Inputs)
	if err != nil {
		return nil, err
	}
	lines = append(lines, files...)
	sort.Strings(lines)
	return append([]string{FPVersion}, lines...), nil
}

// SetupFingerprint hashes cluster_setup_cmd and its inputs; cluster_setup re-runs
// once per cluster whenever it changes. Empty command yields "".
func SetupFingerprint(root string, c *config.Config) (string, error) {
	if strings.TrimSpace(c.Template.ClusterSetupCmd) == "" {
		return "", nil
	}
	files, err := hashInputs(root, c.Template.ClusterSetupInputs)
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	return hashLines(append([]string{FPVersion, "cmd setup " + c.Template.ClusterSetupCmd}, files...)), nil
}

// hashInputs walks each input (file or directory, relative to root) and returns
// "file <rel> <sha256>" lines. .git directories are skipped; a symlink is hashed
// as its target string (not followed). A missing input is a config error (exit 3).
func hashInputs(root string, inputs []string) ([]string, error) {
	var out []string
	for _, in := range inputs {
		base := filepath.Join(root, filepath.Clean(in))
		if _, err := os.Lstat(base); err != nil {
			return nil, errs.Wrap(errs.Config, errs.CodeInputMissing, err, "template input %q", in).
				WithHint("fix [template].inputs in %s", config.FileName)
		}
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			sum, err := hashEntry(p, d)
			if err != nil {
				return err
			}
			out = append(out, fmt.Sprintf("file %s %s", filepath.ToSlash(rel), sum))
			return nil
		})
		if err != nil {
			return nil, errs.Wrap(errs.Config, errs.CodeInputMissing, err, "hash template input %q", in)
		}
	}
	return out, nil
}

// hashEntry hashes a regular file's bytes or a symlink's target string.
func hashEntry(p string, d fs.DirEntry) (string, error) {
	h := sha256.New()
	if d.Type()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		h.Write([]byte("symlink:" + target))
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	if !d.Type().IsRegular() {
		return "", errors.New("unsupported file type: " + p)
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashLines(lines []string) string {
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n") + "\n"))
	return hex.EncodeToString(sum[:])
}
