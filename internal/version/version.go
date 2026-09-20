// Package version carries zorgscope's build identity: the semantic version the footer shows and,
// when the build recorded one, the commit it was built from.
//
// It is its own package rather than a constant in main so that the web layer can render it
// without importing a command, and so that there is exactly one place the number is written down.
package version

import (
	"runtime/debug"
	"sync"
)

// Version is the semantic version of this build (https://semver.org). Bump it here — this
// constant is the single source of truth, and everything that displays a version reads it.
//
// It is a constant rather than an -ldflags variable on purpose: the production image is built by
// deploy/Dockerfile from a source copy with no .git directory, so a version stamped at link time
// would be empty in exactly the build that matters most. The commit below is the part that can
// only come from the toolchain, and it is allowed to be absent.
const Version = "0.6.0"

// shortRevisionLen is how much of the commit hash is shown. Seven characters is what git itself
// abbreviates to and what a person can read back to `git show`.
const shortRevisionLen = 7

var (
	once     sync.Once
	revision string
)

// Revision is the short commit hash this binary was built from, or "" when the build did not
// record one — a `go build` from a source copy without version control, which is how the
// container image is built. Callers must treat the empty string as normal, not as an error.
func Revision() string {
	once.Do(func() { revision = readRevision(debug.ReadBuildInfo) })
	return revision
}

// String is the version as it is displayed: "v0.2.0", with the commit appended as build metadata
// when one is known. The '+' form is semver's own build-metadata syntax, so the string stays a
// valid semantic version either way.
func String() string {
	if rev := Revision(); rev != "" {
		return "v" + Version + "+" + rev
	}
	return "v" + Version
}

// readRevision pulls vcs.revision out of the build info. It takes the reader as an argument so a
// test can exercise both the stamped and the unstamped build without needing two binaries.
func readRevision(read func() (*debug.BuildInfo, bool)) string {
	info, ok := read()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key != "vcs.revision" {
			continue
		}
		if len(s.Value) > shortRevisionLen {
			return s.Value[:shortRevisionLen]
		}
		return s.Value
	}
	return ""
}
