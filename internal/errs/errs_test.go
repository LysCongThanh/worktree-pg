package errs

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("boom"), 1},
		{New(Conflict, CodeForeignResource, "x"), 2},
		{New(Config, CodeConfigInvalid, "x"), 3},
		{New(Prereq, CodeAdminDSNMissing, "x"), 4},
		{New(UserCmd, CodeMigrateFailed, "x"), 5},
		{New(Busy, CodeLeaseHeld, "x"), 6},
		{New(NotFound, CodeNotProvisioned, "x"), 7},
		{New(NotImplemented, CodeNotImplemented, "x"), 1},
		{context.Canceled, 130},
		{fmt.Errorf("wrapped: %w", New(Busy, CodeLockTimeout, "x")), 6},
		{Kind(99).asErr(), 1},
	}
	for i, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("case %d: ExitCode(%v) = %d, want %d", i, c.err, got, c.want)
		}
	}
}

// asErr builds an error with an unknown kind to exercise the default branch.
func (k Kind) asErr() error { return &Error{Kind: k, Code: "X", Msg: "x"} }

func TestWrapAndHint(t *testing.T) {
	cause := errors.New("dial tcp: refused")
	e := Wrap(Prereq, CodePGUnreachable, cause, "cannot reach %s", "127.0.0.1:5432").WithHint("start postgres").WithLog("/tmp/l")
	if e.Error() != "cannot reach 127.0.0.1:5432: dial tcp: refused" {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !errors.Is(e, cause) || e.Hint != "start postgres" || e.LogPath != "/tmp/l" {
		t.Fatalf("unexpected wrap result %+v", e)
	}
	if !Is(fmt.Errorf("ctx: %w", e), CodePGUnreachable) || Is(nil, CodePGUnreachable) {
		t.Fatal("Is() mismatch")
	}
	if CodeOf(errors.New("x")) != CodeInternal || CodeOf(nil) != "" {
		t.Fatal("CodeOf() mismatch")
	}
	if As(nil) != nil {
		t.Fatal("As(nil) should be nil")
	}
}
