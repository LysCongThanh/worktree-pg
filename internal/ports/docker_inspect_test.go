package ports

import (
	"errors"
	"testing"
)

// `docker inspect a gone` exits 1 when container gone was removed after `docker ps`,
// yet prints complete JSON for a: those bound ports must still be excluded (S8).
func TestBoundFromInspectKeepsPartialOutput(t *testing.T) {
	out := []byte(`[{"HostConfig":{"PortBindings":{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"15417"}]}},"NetworkSettings":{"Ports":{}}}]`)
	exit1 := errors.New("exit status 1")
	set, err := boundFromInspect(out, exit1)
	if !set.Has(15417) {
		t.Fatalf("partial inspect output dropped: %v", set)
	}
	if !errors.Is(err, exit1) {
		t.Fatalf("run error must still be reported: %v", err)
	}
	if set, err := boundFromInspect(nil, exit1); len(set) != 0 || !errors.Is(err, exit1) {
		t.Fatalf("no output: set %v err %v", set, err)
	}
	if set, err := boundFromInspect(out, nil); err != nil || !set.Has(15417) {
		t.Fatalf("clean run: set %v err %v", set, err)
	}
}
