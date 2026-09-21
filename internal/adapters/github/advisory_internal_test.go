package github

import (
	"reflect"
	"strings"
	"testing"
)

// Dependabot cites advisories in its release notes, far past the 300 bytes zorgscope keeps as a
// summary. The scan must see the whole body, and these cases are shaped like the real thing.
func TestAdvisoryIDs(t *testing.T) {
	padding := strings.Repeat("Release notes and changelog text. ", 30) // well past 300 bytes

	for _, tc := range []struct {
		name  string
		texts []string
		want  []string
	}{
		{"nothing in an ordinary body", []string{"Fix a typo", "The link on the about page is broken."}, nil},
		{"a CVE far past the summary", []string{"Bump nokogiri", padding + "Fixes CVE-2026-54904."}, []string{"CVE-2026-54904"}},
		{"a GHSA, case normalised", []string{"", padding + "See ghsa-6WX8-w4f5-WWCR for details."}, []string{"GHSA-6wx8-w4f5-wwcr"}},
		{"a CVE in the title", []string{"Patch CVE-2026-1111", ""}, []string{"CVE-2026-1111"}},
		{"a lower-case CVE is stored upper-case", []string{"", "fixes cve-2026-2222"}, []string{"CVE-2026-2222"}},
		{"duplicates collapse, first-seen order kept", []string{"CVE-2026-3333", "CVE-2026-4444 and again CVE-2026-3333"}, []string{"CVE-2026-3333", "CVE-2026-4444"}},
		{"at most three", []string{"", "CVE-2026-0001 CVE-2026-0002 CVE-2026-0003 CVE-2026-0004"}, []string{"CVE-2026-0001", "CVE-2026-0002", "CVE-2026-0003"}},
		{"a CVE needs at least four digits after the year", []string{"", "CVE-2026-12 is not one"}, nil},
	} {
		if got := advisoryIDs(tc.texts...); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: advisoryIDs = %v, want %v", tc.name, got, tc.want)
		}
	}
}
