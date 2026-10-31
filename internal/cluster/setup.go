package cluster

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/config"
	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/pg"
	"github.com/LysCongThanh/worktree-pg/internal/runner"
)

// Setup runs [template].cluster_setup_cmd (cluster-wide roles, e.g. for RLS) once
// per cluster per setup fingerprint. Concurrent ups serialise on an advisory lock
// and re-check Done after acquiring it, so the command runs once.
type Setup struct {
	Admin    *pg.Admin
	Cfg      *config.Config
	Dir      string // worktree toplevel
	Host     string // host for PGHOST
	StateDir string
	LogKey   string
	Wait     time.Duration
	Echo     io.Writer
	Done     func() bool  // reports whether the current setup fingerprint is already recorded
	Record   func() error // records success; called while the lock is still held
}

// Run executes the setup command as the admin against the maintenance database.
// It returns ran=false when another process already completed it.
func (s *Setup) Run(ctx context.Context) (ran bool, err error) {
	if strings.TrimSpace(s.Cfg.Template.ClusterSetupCmd) == "" {
		return false, nil
	}
	key := pg.LockKey("setup/" + s.Cfg.Name)
	deadline := time.Now().Add(s.wait())
	for {
		ok, err := s.Admin.TryLock(ctx, key)
		if err != nil {
			return false, err
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return false, errs.New(errs.Busy, errs.CodeLockTimeout, "timed out waiting for cluster setup of %s", s.Cfg.Name)
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	defer s.Admin.Unlock(context.WithoutCancel(ctx), key) //nolint:errcheck // session end also unlocks
	if s.Done != nil && s.Done() {
		return false, nil
	}
	cred := s.Admin.Cred(s.Cfg.Cluster.MaintenanceDB)
	cred.Host = s.Host
	err = runner.Run(ctx, runner.Cmd{
		Label: "cluster_setup_cmd", Code: errs.CodeSetupFailed, Script: s.Cfg.Template.ClusterSetupCmd,
		Dir: s.Dir, Env: pg.PGEnv(cred), Strip: []string{s.Cfg.Cluster.AdminDSNEnv},
		LogPath: runner.LogPath(s.StateDir, s.LogKey, "cluster_setup_cmd", time.Now()),
		Echo:    s.Echo, Timeout: s.Cfg.Template.Timeout.Duration,
	})
	if err == nil && s.Record != nil {
		err = s.Record() // before Unlock, so a waiter's Done() sees it
	}
	return err == nil, err
}

func (s *Setup) wait() time.Duration {
	if s.Wait > 0 {
		return s.Wait
	}
	return s.Cfg.Template.Timeout.Duration
}
