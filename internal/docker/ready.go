package docker

import (
	"context"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// ReadyTimeout is the default readiness budget: init of a fresh data directory
// took 18-30 s on a loaded machine (spike S6), so allow twice that.
const ReadyTimeout = 60 * time.Second

// readyPoll is the delay between readiness probes.
const readyPoll = 500 * time.Millisecond

// WaitReady calls probe until it succeeds or timeout elapses. probe must be a real
// host-side TCP query with the final credentials (e.g. SELECT 1): the socket-based
// pg_isready answers during the entrypoint's temporary init server (spike D4).
func WaitReady(ctx context.Context, timeout time.Duration, probe func(context.Context) error) error {
	if timeout <= 0 {
		timeout = ReadyTimeout
	}
	deadline := time.Now().Add(timeout)
	var last error
	for {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		last = probe(pctx)
		cancel()
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errs.Wrap(errs.Prereq, errs.CodePGUnreachable, last, "managed postgres not ready after %s", timeout).
				WithHint("check `docker logs` of the worktree-pg cluster container")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPoll):
		}
	}
}
