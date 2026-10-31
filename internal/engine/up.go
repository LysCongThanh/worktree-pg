package engine

import "context"

// Up provisions the worktree idempotently: registry intent, ports, template,
// role + database clone, env files (spec §5 "Clone"). Stub until milestone M2/M3.
func (e *Engine) Up(ctx context.Context, req UpRequest) (*UpResult, error) {
	return nil, notImplemented("up")
}
