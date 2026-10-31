// Package secret derives stable per-role passwords from a machine-local key.
//
// The key lives at <state>/secret.key (32 random bytes, mode 0600). Passwords are
// HMAC-SHA256(key, purpose), so re-running `up` yields the same password without
// ever storing it in state.json (invariant S6) and re-provisioning keeps existing
// env files valid (invariant S11).
package secret

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/LysCongThanh/worktree-pg/internal/errs"
)

// FileName is the key file inside the state directory.
const FileName = "secret.key"

const keyLen = 32

// Secret holds the loaded key. It is never serialised.
type Secret struct{ key []byte }

// LoadOrCreate reads <dir>/secret.key, creating it atomically (temp file + link) on first
// use. A key file readable by group/other is refused (SECRET_KEY_UNSAFE, exit 4).
func LoadOrCreate(dir string) (*Secret, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "create state dir")
	}
	path := filepath.Join(dir, FileName)
	if s, err := load(path); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "generate secret key")
	}
	// Publish atomically: write a private temp file, then hard-link it into place.
	// os.Link fails if the key already exists, so a concurrent loser never sees a
	// half-written file (it reads the winner's complete key instead).
	tmp, err := os.CreateTemp(dir, FileName+".tmp-*")
	if err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "create temp key in %s", dir)
	}
	defer os.Remove(tmp.Name())
	werr := tmp.Chmod(0o600)
	if werr == nil {
		_, werr = tmp.Write([]byte(hex.EncodeToString(key)))
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, werr, "write %s", tmp.Name())
	}
	if err := os.Link(tmp.Name(), path); errors.Is(err, fs.ErrExist) { // lost a creation race: use the winner's key
		return load(path)
	} else if err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "publish %s", path)
	}
	return &Secret{key: key}, nil
}

// load reads and validates an existing key file.
func load(path string) (*Secret, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, errs.New(errs.Prereq, errs.CodeSecretKeyUnsafe, "%s must have mode 0600 (has %o)", path, st.Mode().Perm()).
			WithHint("chmod 600 %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, errs.CodeInternal, err, "read %s", path)
	}
	key, err := hex.DecodeString(string(raw))
	if err != nil || len(key) != keyLen {
		return nil, errs.New(errs.Prereq, errs.CodeSecretKeyUnsafe, "%s is malformed", path).
			WithHint("delete it to regenerate; existing worktree passwords will be rotated on next up")
	}
	return &Secret{key: key}, nil
}

// Password derives a 32-hex-char (128-bit) password for a purpose such as
// "role/<cluster-id>/<role-name>". URL- and shell-safe by construction.
func (s *Secret) Password(purpose string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("worktree-pg/password/v1\x00" + purpose))
	return hex.EncodeToString(m.Sum(nil))[:32]
}
