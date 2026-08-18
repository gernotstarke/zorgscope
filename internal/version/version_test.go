package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

// The version has to be a semantic version, because it is displayed as one and because a
// deployment that cannot be ordered against another is not much use in a footer.
func TestTheVersionIsSemantic(t *testing.T) {
	parts := strings.Split(Version, ".")
	if len(parts) != 3 {
		t.Fatalf("Version = %q, want three dot-separated numbers", Version)
	}
	for _, p := range parts {
		if p == "" || strings.TrimLeft(p, "0123456789") != "" {
			t.Errorf("Version = %q: %q is not a number", Version, p)
		}
	}
}

// A build with no version control recorded — which is exactly how deploy/Dockerfile builds the
// production image, from a source copy with no .git — still produces a usable version rather than
// a string with a dangling '+'.
func TestAnUnstampedBuildStillHasAVersion(t *testing.T) {
	tests := []struct {
		name string
		read func() (*debug.BuildInfo, bool)
	}{
		{"no build info at all", func() (*debug.BuildInfo, bool) { return nil, false }},
		{"build info without vcs settings", func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{}, true
		}},
		{"build info with other settings", func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "GOARCH", Value: "arm64"}}}, true
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := readRevision(tc.read); got != "" {
				t.Errorf("readRevision = %q, want empty", got)
			}
		})
	}
}

// A stamped build shows the commit, abbreviated to what git itself would print.
func TestAStampedBuildIsAbbreviated(t *testing.T) {
	const full = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
	read := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.time", Value: "2026-08-17T10:00:00Z"},
			{Key: "vcs.revision", Value: full},
		}}, true
	}
	got := readRevision(read)
	if got != full[:shortRevisionLen] {
		t.Errorf("readRevision = %q, want %q", got, full[:shortRevisionLen])
	}
}

// A revision shorter than the abbreviation is returned whole rather than sliced out of range.
func TestAShortRevisionIsNotTruncated(t *testing.T) {
	read := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}}}, true
	}
	if got := readRevision(read); got != "abc" {
		t.Errorf("readRevision = %q, want %q", got, "abc")
	}
}

// What the footer actually shows. Whether this test's own binary carries a revision depends on
// how it was built, so the assertion is on the shape both forms share rather than on one of them.
func TestStringIsTheVersionPossiblyWithACommit(t *testing.T) {
	got := String()
	if !strings.HasPrefix(got, "v"+Version) {
		t.Fatalf("String() = %q, want it to start with %q", got, "v"+Version)
	}
	rest := strings.TrimPrefix(got, "v"+Version)
	switch {
	case rest == "":
	case strings.HasPrefix(rest, "+") && len(rest) > 1:
	default:
		t.Errorf("String() = %q: the trailing %q is neither absent nor a build-metadata suffix", got, rest)
	}
}
