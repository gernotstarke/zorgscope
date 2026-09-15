// This file tests clip from inside the package: the limits it enforces are internal, and testing
// them through a whole refresh run would need a database to say something about a pure function.
package refresh

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// clip is what keeps an upstream that answers with a page of HTML out of the database and off the
// dashboard: github.IssueFetcher joins one error per failing repository, so a GitHub-wide outage
// across a dozen repositories produces a multi-kilobyte message that would otherwise go verbatim
// into source_state.last_error and onto the tile.
func TestClipShortensOnlyWhatIsTooLong(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "shorter than the limit is untouched", in: "boom", n: 10, want: "boom"},
		{name: "exactly the limit is untouched", in: "boom", n: 4, want: "boom"},
		{name: "one byte over is cut and marked", in: "boomb", n: 4, want: "boom…"},
		{name: "empty stays empty", in: "", n: 4, want: ""},
		{name: "zero limit clips everything", in: "boom", n: 0, want: "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clip(tt.in, tt.n); got != tt.want {
				t.Errorf("clip(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

func TestClipCutsALongMessageToTheLimit(t *testing.T) {
	long := strings.Repeat("a", 5000)

	got := clip(long, maxErrLen)

	if len(got) != maxErrLen+len("…") {
		t.Errorf("len(clip(5000 bytes, %d)) = %d, want %d — a long error must be cut, not stored whole",
			maxErrLen, len(got), maxErrLen+len("…"))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("clip(...) = %q…, want a trailing … marking that it was shortened", got[:20])
	}
}

// The cut must land on a rune boundary: an error carrying non-ASCII text (an upstream message, a
// repository name) is stored and rendered, and half a rune is neither.
func TestClipKeepsTheResultValidUTF8(t *testing.T) {
	// Three-byte runes, so a naive cut at maxErrLen (500, not a multiple of three) would split one.
	long := strings.Repeat("☃", 5000)

	got := clip(long, maxErrLen)

	if !utf8.ValidString(got) {
		t.Errorf("clip cut mid-rune: %q is not valid UTF-8", got)
	}
	if len(got) > maxErrLen+len("…") {
		t.Errorf("len(clip(...)) = %d, want at most %d", len(got), maxErrLen+len("…"))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a shortened message must be marked as shortened")
	}
}
