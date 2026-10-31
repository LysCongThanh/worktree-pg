package ports

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

func TestParseRange(t *testing.T) {
	good := map[string]Range{"5600-5999": {5600, 5999}, " 7000 ": {7000, 7000}, "1024-1024": {1024, 1024}}
	for in, want := range good {
		if got, err := ParseRange(in); err != nil || got != want {
			t.Errorf("ParseRange(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "a-b", "80-90", "6000-5000", "60000-70000", "1-"} {
		if _, err := ParseRange(bad); err == nil {
			t.Errorf("ParseRange(%q) accepted", bad)
		}
	}
	if _, err := ParseRanges([]string{"5600-5700", "x"}); err == nil {
		t.Error("ParseRanges should fail")
	}
	if !(Range{5600, 5700}).Overlaps(Range{5700, 5800}) || (Range{1, 2}).Overlaps(Range{3, 4}) {
		t.Error("Overlaps")
	}
	if r, err := ParseProcRange("32768\t60999\n"); err != nil || r != DefaultEphemeral {
		t.Errorf("ParseProcRange = %v %v", r, err)
	}
	for _, bad := range []string{"", "1", "a b", "9 3"} {
		if _, err := ParseProcRange(bad); err == nil {
			t.Errorf("ParseProcRange(%q) accepted", bad)
		}
	}
	if e := Ephemeral(); e.Lo < 1024 || e.Hi > 65535 {
		t.Errorf("Ephemeral = %v", e)
	}
}

// TestInvariant_S8_PortAllocation: taken, reserved, ephemeral and busy ports are
// skipped; sticky ports are kept (busy = warning, never kill).
func TestInvariant_S8_PortAllocation(t *testing.T) {
	base := map[string][]Range{DefaultKey: {{5600, 5609}}}
	cases := []struct {
		name    string
		a       Allocator
		vars    []string
		sticky  map[string]int
		check   func(t *testing.T, got map[string]int, warns []Warning)
		wantErr string
	}{
		{name: "deterministic distinct", a: Allocator{Ranges: base}, vars: []string{"PORT", "ADMIN_PORT", "API"},
			check: func(t *testing.T, got map[string]int, _ []Warning) {
				seen := PortSet{}
				for v, p := range got {
					if p < 5600 || p > 5609 || seen.Has(p) {
						t.Errorf("%s=%d out of range or duplicate", v, p)
					}
					seen.Add(p)
				}
			}},
		{name: "exclusions honored", a: Allocator{Ranges: base, Exclude: PortSet{}.Add(5600, 5601, 5602, 5603, 5604, 5605, 5606, 5607, 5608)},
			vars: []string{"PORT"}, check: func(t *testing.T, got map[string]int, _ []Warning) {
				if got["PORT"] != 5609 {
					t.Errorf("PORT=%d want 5609", got["PORT"])
				}
			}},
		{name: "probe busy skipped", a: Allocator{Ranges: base, Probe: func(p int) bool { return p == 5603 }},
			vars: []string{"PORT"}, check: func(t *testing.T, got map[string]int, _ []Warning) {
				if got["PORT"] != 5603 {
					t.Errorf("PORT=%d want 5603", got["PORT"])
				}
			}},
		{name: "sticky busy kept with warning", a: Allocator{Ranges: base, Probe: func(int) bool { return false }},
			vars: []string{"PORT"}, sticky: map[string]int{"PORT": 5605},
			check: func(t *testing.T, got map[string]int, w []Warning) {
				if got["PORT"] != 5605 || len(w) != 1 || w[0].Code != CodePortBusy {
					t.Errorf("got %v warns %v", got, w)
				}
			}},
		{name: "sticky now excluded is reassigned", a: Allocator{Ranges: base, Exclude: PortSet{}.Add(5605)},
			vars: []string{"PORT"}, sticky: map[string]int{"PORT": 5605},
			check: func(t *testing.T, got map[string]int, _ []Warning) {
				if got["PORT"] == 5605 {
					t.Error("excluded sticky port kept")
				}
			}},
		{name: "ephemeral and privileged excluded", a: Allocator{Ranges: map[string][]Range{DefaultKey: {{32760, 32770}}}, Ephemeral: Range{32768, 60999}},
			vars: []string{"A", "B", "C", "D", "E", "F", "G", "H"},
			check: func(t *testing.T, got map[string]int, _ []Warning) {
				for v, p := range got {
					if p >= 32768 {
						t.Errorf("%s=%d inside ephemeral range", v, p)
					}
				}
			}},
		{name: "per-var override", a: Allocator{Ranges: map[string][]Range{DefaultKey: {{5600, 5609}}, "ADMIN": {{7000, 7000}}}},
			vars: []string{"ADMIN", "PORT"}, check: func(t *testing.T, got map[string]int, _ []Warning) {
				if got["ADMIN"] != 7000 {
					t.Errorf("ADMIN=%d", got["ADMIN"])
				}
			}},
		{name: "exhausted", a: Allocator{Ranges: map[string][]Range{DefaultKey: {{5600, 5600}}}}, vars: []string{"A", "B"}, wantErr: errs.CodePortsExhausted},
		{name: "no ranges", a: Allocator{}, vars: []string{"A"}, wantErr: errs.CodePortsExhausted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, warns, err := c.a.Allocate("key1", c.vars, c.sticky)
			if c.wantErr != "" {
				if errs.CodeOf(err) != c.wantErr || errs.ExitCode(err) != 3 {
					t.Fatalf("err = %v, want %s", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, got, warns)
			again, _, _ := c.a.Allocate("key1", c.vars, c.sticky)
			for v := range got {
				if again[v] != got[v] {
					t.Errorf("not deterministic for %s", v)
				}
			}
		})
	}
}

func TestListenProbeDetectsBoundPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := l.Addr().(*net.TCPAddr).Port
	if ListenProbe("127.0.0.1")(p) {
		t.Fatalf("port %d reported free while bound", p)
	}
	l2, _ := net.Listen("tcp", "127.0.0.1:0")
	free := l2.Addr().(*net.TCPAddr).Port
	l2.Close()
	if !ListenProbe("127.0.0.1")(free) {
		t.Logf("port %s busy (racy, ignored)", strconv.Itoa(free))
	}
}

func TestParseDockerBindings(t *testing.T) {
	raw := `[{"HostConfig":{"PortBindings":{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"15421"}]}},"NetworkSettings":{"Ports":{}}},
	{"HostConfig":{"PortBindings":{"9000/tcp":[{"HostIp":"","HostPort":""}]}},"NetworkSettings":{"Ports":{"9000/tcp":[{"HostIp":"0.0.0.0","HostPort":"33653"}],"9001/tcp":null}}}]`
	set, err := ParseDockerBindings([]byte(raw))
	if err != nil || !set.Has(15421) || !set.Has(33653) || len(set) != 2 {
		t.Fatalf("set=%v err=%v", set, err)
	}
	if _, err := ParseDockerBindings([]byte("{")); err == nil {
		t.Fatal("expected error")
	}
	if _, err := DockerBound(context.Background()); err != nil {
		t.Logf("docker unavailable (best effort): %v", err)
	}
}
