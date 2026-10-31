package gitx

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Worktree is one record of `git worktree list --porcelain -z`.
type Worktree struct {
	Path           string `json:"path"`
	HEAD           string `json:"head,omitempty"`
	Branch         string `json:"branch,omitempty"` // short name, "" when detached/bare
	Bare           bool   `json:"bare,omitempty"`
	Detached       bool   `json:"detached,omitempty"`
	Locked         bool   `json:"locked,omitempty"`
	LockReason     string `json:"lock_reason,omitempty"`
	Prunable       bool   `json:"prunable,omitempty"`
	PrunableReason string `json:"prunable_reason,omitempty"`
}

// ParsePorcelainZ parses NUL-separated porcelain output (git 2.36+). Attributes
// are NUL-terminated and records end with an extra NUL (spike S9, git 2.55):
//
//	worktree <path>\0HEAD <sha>\0branch refs/heads/x\0[locked [reason]\0][prunable reason\0]\0
//
// Paths may contain spaces and newlines; -z keeps them intact.
func ParsePorcelainZ(b []byte) ([]Worktree, error) {
	var out []Worktree
	var cur *Worktree
	for _, f := range strings.Split(string(b), "\x00") {
		if f == "" { // end of record (or trailing terminator)
			if cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			continue
		}
		key, val, _ := strings.Cut(f, " ")
		if key == "worktree" {
			if cur != nil {
				return nil, fmt.Errorf("porcelain: record for %q not terminated", cur.Path)
			}
			cur = &Worktree{Path: val}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("porcelain: attribute %q before worktree line", f)
		}
		switch key {
		case "HEAD":
			cur.HEAD = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "bare":
			cur.Bare = true
		case "detached":
			cur.Detached = true
		case "locked":
			cur.Locked, cur.LockReason = true, val
		case "prunable":
			cur.Prunable, cur.PrunableReason = true, val
		}
		// unknown attributes from future git versions are ignored
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out, nil
}

// Worktrees lists all worktrees of the repository.
func (r *Repo) Worktrees(ctx context.Context) ([]Worktree, error) {
	out, err := gitRaw(ctx, r.Toplevel, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return ParsePorcelainZ(out)
}

// Find returns the listed worktree whose (symlink-resolved) path equals path.
// The first listed worktree is the main one; index reports its position.
func Find(list []Worktree, path string) (wt Worktree, index int, ok bool) {
	want := resolve(path)
	for i, w := range list {
		if resolve(w.Path) == want {
			return w, i, true
		}
	}
	return Worktree{}, -1, false
}

// resolve cleans a path and resolves symlinks when it exists.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
