package pg

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// scramIterations matches PostgreSQL's default scram_iterations (4096).
const scramIterations = 4096

// ScramVerifier returns the SCRAM-SHA-256 verifier PostgreSQL stores for
// password (RFC 5802/7677, the format of pg_authid.rolpassword). Role passwords are
// always sent as this verifier, never as plaintext: CREATE/ALTER ROLE statement
// text reaches the server log on any error (log_min_error_statement), and
// pg_stat_activity / pg_stat_statements while it runs (invariant S6). The server
// stores a verifier verbatim, whatever password_encryption says.
//
// The salt is derived from role and password, so re-applying the same password
// leaves the catalog byte-identical (idempotent re-runs, S11) while two roles with
// the same password still get different verifiers. Generated passwords are ASCII,
// so SASLprep is the identity.
func ScramVerifier(role, password string) (string, error) {
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte("worktree-pg scram salt\x00" + role))
	salt := mac.Sum(nil)[:16]
	salted, err := pbkdf2.Key(sha256.New, password, salt, scramIterations, sha256.Size)
	if err != nil {
		return "", err
	}
	clientKey := hmacSHA256(salted, "Client Key")
	stored := sha256.Sum256(clientKey)
	server := hmacSHA256(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", scramIterations, b64(salt), b64(stored[:]), b64(server)), nil
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
