package cli

import (
	"runtime/debug"
	"strings"
)

// resolveVersion returns the version stamped by the release build (-ldflags -X
// ...cli.Version) or, for `go install module@vX.Y.Z` builds that carry no
// ldflags, the module version Go records in the binary's build info. Both are
// printed without the leading "v" so release and go-install builds agree.
func resolveVersion(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != "dev" && stamped != "" {
		return stamped
	}
	if ok && info != nil {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}

// currentVersion is what `worktree-pg version` prints.
func currentVersion() string {
	info, ok := debug.ReadBuildInfo()
	return resolveVersion(Version, info, ok)
}
