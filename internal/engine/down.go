package engine

import "context"

// Down tears the worktree down idempotently: DROP DATABASE/ROLE (markers must
// match), strip env blocks, delete the registry entry (spec §5 "Teardown"). Stub until M3.
func (e *Engine) Down(ctx context.Context, req DownRequest) (*DownResult, error) {
	return nil, notImplemented("down")
}
