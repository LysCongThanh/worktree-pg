package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	cases := []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"version"}, 0, "worktree-pg dev"},
		{[]string{"help"}, 0, ""},
		{nil, 3, ""},
		{[]string{"up"}, 1, ""},
		{[]string{"bogus"}, 3, ""},
	}
	for _, c := range cases {
		var o, e bytes.Buffer
		if got := run(c.args, &o, &e); got != c.code || !strings.Contains(o.String(), c.out) {
			t.Errorf("run(%v) = %d out=%q err=%q", c.args, got, o.String(), e.String())
		}
	}
}
