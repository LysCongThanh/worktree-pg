package engine

import (
	"context"

	"github.com/LysCongThanh/worktree-pg/internal/envfile"
)

// Env returns the variables of an already provisioned worktree from the registry;
// it never provisions (NOT_PROVISIONED, exit 7). Stub until M3.
func (e *Engine) Env(ctx context.Context, path string) ([]envfile.Var, error) {
	return nil, notImplemented("env")
}
