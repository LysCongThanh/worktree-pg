package engine

import "context"

// GC reconciles the registry with `git worktree list` and drops orphans (dry run
// unless req.Apply, invariant S13). Stub until M6.
func (e *Engine) GC(ctx context.Context, req GCRequest) (*GCReport, error) {
	return nil, notImplemented("gc")
}
