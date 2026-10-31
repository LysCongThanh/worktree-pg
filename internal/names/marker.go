package names

import (
	"encoding/json"
	"strings"
)

// MarkerPrefix starts every ownership marker. Markers are stored as a database or
// role COMMENT and as a docker label value; they are the second source of truth
// (after state.json) proving an object was created by worktree-pg (invariant S1).
const MarkerPrefix = "worktree-pg:v1 "

// LabelKey is the docker label key holding the marker on managed containers.
const LabelKey = "worktree-pg"

// Marker kinds.
const (
	KindClone         = "clone"
	KindRole          = "role"
	KindOwner         = "owner"
	KindTemplate      = "template"
	KindTemplateBuild = "template-build"
	KindCluster       = "cluster"
)

// Marker identifies the worktree (or template) an object belongs to.
type Marker struct {
	Kind string `json:"kind"`
	Key  string `json:"key,omitempty"`
	Path string `json:"path,omitempty"`
	FP   string `json:"fp,omitempty"`
}

// String renders the marker as `worktree-pg:v1 {"kind":...}`.
func (m Marker) String() string {
	b, _ := json.Marshal(m) // marshalling a struct of strings cannot fail
	return MarkerPrefix + string(b)
}

// ParseMarker decodes a COMMENT/label value. ok is false for foreign or malformed text.
func ParseMarker(s string) (m Marker, ok bool) {
	if !strings.HasPrefix(s, MarkerPrefix) {
		return Marker{}, false
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(s, MarkerPrefix)), &m); err != nil || m.Kind == "" {
		return Marker{}, false
	}
	return m, true
}

// Owns reports whether the marker text belongs to the given kind and worktree key.
func Owns(comment, kind, key string) bool {
	m, ok := ParseMarker(comment)
	return ok && m.Kind == kind && m.Key == key
}
