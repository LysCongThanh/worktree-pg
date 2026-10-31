package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
	"github.com/LysCongThanh/worktree-pg/internal/lockfile"
)

// File names inside the state directory.
const (
	StateFile = "state.json"
	LockFile  = "state.lock"
)

// DefaultLockTimeout bounds how long Update waits for the registry lock (exit 6 after).
const DefaultLockTimeout = 10 * time.Second

// Store reads and writes state.json under Dir.
type Store struct {
	Dir         string
	LockTimeout time.Duration
}

// StateDir resolves the state directory: $WORKTREE_PG_STATE_DIR, then
// $XDG_STATE_HOME/worktree-pg, then ~/.local/state/worktree-pg.
func StateDir(getenv func(string) string) (string, error) {
	if d := getenv("WORKTREE_PG_STATE_DIR"); d != "" {
		return filepath.Abs(d)
	}
	if d := getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "worktree-pg"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errs.Wrap(errs.Prereq, errs.CodeInternal, err, "cannot determine home directory")
	}
	return filepath.Join(home, ".local", "state", "worktree-pg"), nil
}

// Open prepares dir (mode 0700) and returns a Store.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "create state dir %s", dir)
	}
	return &Store{Dir: dir, LockTimeout: DefaultLockTimeout}, nil
}

// View runs fn on a snapshot of the state under a shared lock. Mutations made by
// fn are discarded.
func (s *Store) View(fn func(*State) error) error {
	l, err := lockfile.Acquire(context.Background(), s.path(LockFile), false, s.timeout())
	if err != nil {
		return err
	}
	defer l.Release()
	st, err := s.read()
	if err != nil {
		return err
	}
	return fn(st)
}

// Update runs fn under the exclusive lock and persists the state atomically if fn
// returns nil. fn must be fast: no network, docker or user commands (S10).
func (s *Store) Update(ctx context.Context, fn func(*State) error) error {
	l, err := lockfile.Acquire(ctx, s.path(LockFile), true, s.timeout())
	if err != nil {
		return err
	}
	defer l.Release()
	st, err := s.read()
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	return s.write(st)
}

func (s *Store) path(name string) string { return filepath.Join(s.Dir, name) }

func (s *Store) timeout() time.Duration {
	if s.LockTimeout > 0 {
		return s.LockTimeout
	}
	return DefaultLockTimeout
}

// read loads state.json; a missing file is an empty state.
func (s *Store) read() (*State, error) {
	b, err := os.ReadFile(s.path(StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "read state")
	}
	st := newState()
	if err := json.Unmarshal(b, st); err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeStateCorrupt, err, "parse %s", s.path(StateFile)).
			WithHint("inspect or move the file aside; DB/role COMMENT markers allow `gc` to recover")
	}
	if st.Version != Version {
		return nil, errs.New(errs.Internal, errs.CodeStateCorrupt, "unsupported state version %d", st.Version)
	}
	if st.Clusters == nil {
		st.Clusters = map[string]*Cluster{}
	}
	if st.Worktrees == nil {
		st.Worktrees = map[string]*Worktree{}
	}
	return st, nil
}

// write persists st via tmp + fsync + rename + directory fsync.
func (s *Store) write(st *State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return errs.Wrap(errs.Internal, errs.CodeInternal, err, "encode state")
	}
	f, err := os.CreateTemp(s.Dir, ".state-*.json")
	if err != nil {
		return errs.Wrap(errs.Internal, errs.CodeInternal, err, "create temp state")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0o600); err == nil {
		if _, err = f.Write(append(b, '\n')); err == nil {
			err = f.Sync()
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, s.path(StateFile))
	}
	if err != nil {
		return errs.Wrap(errs.Internal, errs.CodeInternal, err, "write state")
	}
	if d, err := os.Open(s.Dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
