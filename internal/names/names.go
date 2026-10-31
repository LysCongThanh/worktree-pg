// Package names derives every identifier worktree-pg creates: worktree keys,
// slugs, database/role/template names and managed container names.
//
// All functions are pure and deterministic so the same worktree always maps to
// the same objects (idempotent re-runs, invariant S11).
package names

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxIdent is PostgreSQL's NAMEDATALEN-1: identifiers longer than this are truncated by the server.
const MaxIdent = 63

// MaxApp and MaxSlug bound the variable parts of generated names.
const (
	MaxApp  = 20
	MaxSlug = 20
)

var appRe = regexp.MustCompile(`^[a-z0-9_]+$`)

// ValidApp checks the configured application name ([a-z0-9_], 1..20 bytes).
func ValidApp(app string) error {
	if app == "" || len(app) > MaxApp || !appRe.MatchString(app) {
		return fmt.Errorf("name %q must match [a-z0-9_] and be 1..%d bytes", app, MaxApp)
	}
	return nil
}

// Key returns the stable worktree key: hex(sha256(realpath(toplevel)))[:16].
// The caller must pass the already symlink-resolved, absolute toplevel path.
func Key(realToplevel string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(realToplevel)))
	return hex.EncodeToString(sum[:])[:16]
}

// Slug derives a short readable token from a worktree path's basename:
// lowercased, every run of chars outside [a-z0-9] collapsed into "_", trimmed,
// at most MaxSlug bytes; "wt" when nothing usable remains.
func Slug(path string) string {
	base := strings.ToLower(filepath.Base(filepath.Clean(path)))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
		} else if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	s := strings.Trim(b.String(), "_")
	if len(s) > MaxSlug {
		s = strings.TrimRight(s[:MaxSlug], "_")
	}
	if s == "" {
		return "wt"
	}
	return s
}

// Database returns the per-worktree database (and role) name: <app>_wt_<slug>_<key8>.
func Database(app, slug, key string) string {
	return Fit63(fmt.Sprintf("%s_wt_%s_%s", app, slug, short(key)))
}

// Role returns the per-worktree role name; identical to the database name by design.
func Role(app, slug, key string) string { return Database(app, slug, key) }

// OwnerRole returns the role that owns template objects: <app>_wt_owner.
func OwnerRole(app string) string { return Fit63(app + "_wt_owner") }

// Template returns the published template database name: <app>_tpl_<fp8>.
func Template(app, fingerprint string) string {
	return Fit63(fmt.Sprintf("%s_tpl_%s", app, short(fingerprint)))
}

// TemplateBuild returns the in-progress build database name: <app>_tpl_<fp8>_build.
func TemplateBuild(app, fingerprint string) string {
	return Fit63(Template(app, fingerprint) + "_build")
}

// Container returns the managed cluster container name for an image: wtpg-cluster-<imageslug>.
func Container(image string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(image) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return "wtpg-cluster-" + strings.Trim(b.String(), "-")
}

// Fit63 keeps s within MaxIdent bytes. Longer strings become s[:54]+"_"+sha256(s)[:8],
// which stays unique per input and exactly 63 bytes long.
func Fit63(s string) string {
	if len(s) <= MaxIdent {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return s[:54] + "_" + hex.EncodeToString(sum[:])[:8]
}

// short returns the first 8 chars of a hex digest (or the whole string if shorter).
func short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
