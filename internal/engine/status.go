package engine

import "context"

// Status reports registered worktrees and, when reachable, live object health. Stub until M3.
func (e *Engine) Status(ctx context.Context, req StatusRequest) ([]WorktreeStatus, error) {
	return nil, notImplemented("status")
}
