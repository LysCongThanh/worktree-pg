// Package envfile writes worktree-pg variables into a marked block inside env
// files (.env, .env.local, ...). Everything outside the block is preserved byte for
// byte; the block is the only region worktree-pg ever edits (invariant S7).
package envfile

import (
	"sort"
	"strings"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// Block markers. They are matched after trimming whitespace and a trailing "\r".
const (
	BeginMarker = "# >>> worktree-pg managed block (do not edit) >>>"
	EndMarker   = "# <<< worktree-pg managed block <<<"
)

// Var is one variable written to env files or printed by `env`.
type Var struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"` // contains a password; redact in human output
}

// Splice returns old with the managed block replaced by vars (or appended when
// absent). Line endings follow the file (CRLF if it contains any "\r\n").
// dupKeys lists keys of vars that are also assigned outside the block.
func Splice(old []byte, vars []Var) (out []byte, dupKeys []string, err error) {
	text := string(old)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(text, "\n")
	b, e, err := findBlock(lines)
	if err != nil {
		return nil, nil, err
	}
	dupKeys = duplicates(lines, b, e, vars)
	block := append(append([]string{BeginMarker}, RenderDotenv(vars)...), EndMarker)
	if b >= 0 {
		if eol == "\r\n" {
			for i := range block {
				block[i] += "\r"
			}
		}
		res := append(append(append([]string{}, lines[:b]...), block...), lines[e+1:]...)
		return []byte(strings.Join(res, "\n")), dupKeys, nil
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += eol
	}
	return []byte(text + strings.Join(block, eol) + eol), dupKeys, nil
}

// Strip removes the managed block. found is false (and out == old) when there is none.
func Strip(old []byte) (out []byte, found bool, err error) {
	lines := strings.Split(string(old), "\n")
	b, e, err := findBlock(lines)
	if err != nil || b < 0 {
		return old, false, err
	}
	res := append(append([]string{}, lines[:b]...), lines[e+1:]...)
	return []byte(strings.Join(res, "\n")), true, nil
}

// findBlock locates exactly one well-formed block. (-1,-1) means no block.
func findBlock(lines []string) (begin, end int, err error) {
	begin, end = -1, -1
	for i, l := range lines {
		switch strings.TrimSpace(strings.TrimSuffix(l, "\r")) {
		case BeginMarker:
			if begin >= 0 {
				return 0, 0, corrupt("two begin markers")
			}
			begin = i
		case EndMarker:
			if end >= 0 || begin < 0 {
				return 0, 0, corrupt("end marker without a matching begin marker")
			}
			end = i
		}
	}
	if begin >= 0 && end < 0 {
		return 0, 0, corrupt("begin marker without an end marker")
	}
	return begin, end, nil
}

func corrupt(why string) error {
	return errs.New(errs.Conflict, errs.CodeEnvBlockCorrupt, "managed env block is corrupt: %s", why).
		WithHint("fix or delete the worktree-pg block markers by hand, then re-run")
}

// duplicates finds keys of vars assigned on lines outside [b,e].
func duplicates(lines []string, b, e int, vars []Var) []string {
	want := map[string]bool{}
	for _, v := range vars {
		want[v.Key] = true
	}
	seen := map[string]bool{}
	for i, l := range lines {
		if b >= 0 && i >= b && i <= e {
			continue
		}
		if k := lineKey(l); k != "" && want[k] {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// lineKey returns the assigned key of a dotenv line ("export K=v" or "K=v"), or "".
func lineKey(l string) string {
	l = strings.TrimSpace(strings.TrimSuffix(l, "\r"))
	if l == "" || strings.HasPrefix(l, "#") {
		return ""
	}
	l = strings.TrimPrefix(l, "export ")
	k, _, ok := strings.Cut(l, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(k)
}
