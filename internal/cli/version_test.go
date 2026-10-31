package cli

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	mod := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	cases := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"ldflags win over build info", "0.1.0", mod("v9.9.9"), true, "0.1.0"},
		{"go install tag", "dev", mod("v0.1.0-rc.1"), true, "0.1.0-rc.1"},
		{"local checkout build", "dev", mod("(devel)"), true, "dev"},
		{"no build info", "dev", nil, false, "dev"},
		{"empty module version", "dev", mod(""), true, "dev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVersion(c.stamped, c.info, c.ok); got != c.want {
				t.Fatalf("resolveVersion(%q) = %q, want %q", c.stamped, got, c.want)
			}
		})
	}
}
