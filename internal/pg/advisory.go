package pg

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
)

// LockKey derives the second advisory-lock key from an object name:
// int32(first 4 bytes of sha256(name)). Paired with AdvisoryClass.
func LockKey(name string) int32 {
	sum := sha256.Sum256([]byte(name))
	return int32(binary.BigEndian.Uint32(sum[:4]))
}

// TryLock tries the session-level advisory lock (AdvisoryClass, key) without waiting.
func (a *Admin) TryLock(ctx context.Context, key int32) (bool, error) {
	var ok bool
	err := a.conn.QueryRow(ctx, "select pg_try_advisory_lock($1::int4, $2::int4)", AdvisoryClass, key).Scan(&ok)
	return ok, Wrap(err, "advisory lock")
}

// Unlock releases a lock taken by TryLock. Closing the session also releases it.
func (a *Admin) Unlock(ctx context.Context, key int32) error {
	_, err := a.conn.Exec(ctx, "select pg_advisory_unlock($1::int4, $2::int4)", AdvisoryClass, key)
	return Wrap(err, "advisory unlock")
}
