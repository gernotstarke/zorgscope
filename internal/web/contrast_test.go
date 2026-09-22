package web

import (
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/config"
)

// FR-1.8 AC5 and FR-1.5 AC2: every site colour keeps text readable. White on each heading band, and
// the page's text, muted and link colours on each tile's wash, reach at least 4.5:1 in both
// appearances. When one fails, lower that appearance's --tile-wash-* percentage in app.css; never
// change a brand colour.
func TestTileColoursKeepTextReadable(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	surface, ok := lightDarkToken(t, css, "surface")
	if !ok {
		t.Fatal("no --surface token: every wash below is measured against it")
	}
	foregrounds := map[string][2]rgb{}
	for _, name := range []string{"text", "muted", "accent"} {
		v, ok := lightDarkToken(t, css, name)
		if !ok {
			t.Fatalf("no --%s token: every wash below is measured against it", name)
		}
		foregrounds["--"+name] = v
	}
	washLight, ok := percentToken(t, css, "tile-wash-light")
	if !ok {
		t.Fatal("no --tile-wash-light token")
	}
	washDark, ok := percentToken(t, css, "tile-wash-dark")
	if !ok {
		t.Fatal("no --tile-wash-dark token")
	}
	white := rgb{255, 255, 255}

	for _, key := range config.HueKeys {
		if !strings.Contains(css, ".hue-"+key+" {") {
			t.Errorf("app.css defines no .hue-%s class", key)
		}
		band, ok := hexToken(t, css, "hue-"+key)
		if !ok {
			continue
		}
		sig, ok := hexToken(t, css, "hue-"+key+"-sig")
		if !ok {
			continue
		}
		if r := contrastRatio(white, band); r < 4.5 {
			t.Errorf("white on the %s band = %.2f:1, want at least 4.5:1", key, r)
		}
		light := mixSRGB(band, surface[0], washLight)
		dark := mixSRGB(sig, surface[1], washDark)
		for name, fg := range foregrounds {
			if r := contrastRatio(fg[0], light); r < 4.5 {
				t.Errorf("%s on the light %s wash = %.2f:1, want at least 4.5:1", name, key, r)
			}
			if r := contrastRatio(fg[1], dark); r < 4.5 {
				t.Errorf("%s on the dark %s wash = %.2f:1, want at least 4.5:1", name, key, r)
			}
		}
	}
}

// FR-1.8 AC5: each .hue-<key> class must feed --tile-hue and --tile-sig from its OWN key's
// tokens, not another key's. This is the seam between the tile markup (Task 4) and the token
// values (Task 5) that TestTileColoursKeepTextReadable cannot see, because it reads the
// --hue-<key> tokens directly rather than through the class: a transposition such as
//
//	.hue-teal { --tile-hue: var(--hue-plum); --tile-sig: var(--hue-plum-sig); }
//
// would keep every other test in this file green.
func TestHueClassBindsItsOwnTokens(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	for _, key := range config.HueKeys {
		body := hueClassBody(t, css, key)
		if body == "" {
			continue
		}
		hue := regexp.MustCompile(`--tile-hue:\s*var\(--hue-` + regexp.QuoteMeta(key) + `\)`)
		sig := regexp.MustCompile(`--tile-sig:\s*var\(--hue-` + regexp.QuoteMeta(key) + `-sig\)`)
		if !hue.MatchString(body) {
			t.Errorf(".hue-%s does not set --tile-hue from var(--hue-%s): %q", key, key, body)
		}
		if !sig.MatchString(body) {
			t.Errorf(".hue-%s does not set --tile-sig from var(--hue-%s-sig): %q", key, key, body)
		}
	}
}

// hueClassBody returns the declaration block of `.hue-<key> { ... }` in css, or "" (having
// reported it) when app.css defines no such rule. Tolerant of whitespace, since the rule is
// written on one line today but need not stay that way.
func hueClassBody(t *testing.T, css, key string) string {
	t.Helper()
	m := regexp.MustCompile(`\.hue-` + regexp.QuoteMeta(key) + `\s*\{([^}]*)\}`).FindStringSubmatch(css)
	if m == nil {
		t.Errorf("app.css defines no .hue-%s rule", key)
		return ""
	}
	return m[1]
}

// FR-1.8 AC5: the wash's light-dark() must put --tile-hue with --tile-wash-light in the light
// slot and --tile-sig with --tile-wash-dark in the dark slot. Both orderings happen to clear
// 4.5:1 today (TestTileColoursKeepTextReadable passes either way), so only this test tells them
// apart; swapping the slots would lose design intent, not accessibility.
func TestTileWashUsesHueForLightAndSigForDark(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	//nolint:misspell // color-mix is the CSS function's own name, not prose to be normalised
	want := regexp.MustCompile(
		`light-dark\(\s*color-mix\(in srgb,\s*var\(--tile-hue\)\s*var\(--tile-wash-light\)\s*,\s*var\(--surface\)\s*\)\s*,\s*` +
			`color-mix\(in srgb,\s*var\(--tile-sig\)\s*var\(--tile-wash-dark\)\s*,\s*var\(--surface\)\s*\)\s*\)`)
	if !want.MatchString(css) {
		t.Error(".tile-body's light-dark() does not put --tile-hue with --tile-wash-light in the " +
			"light slot and --tile-sig with --tile-wash-dark in the dark slot")
	}
}

// FR-1.8 AC4: the view switch's current segment paints its background from --accent and its text
// from --bg — the one new colour pair on this branch no other test here covers. The final review
// measured 5.70:1 light and 7.21:1 dark; this pins it so it stays that way.
func TestViewSwitchCurrentSegmentKeepsTextReadable(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	bg, ok := lightDarkToken(t, css, "bg")
	if !ok {
		t.Fatal("no --bg token")
	}
	accent, ok := lightDarkToken(t, css, "accent")
	if !ok {
		t.Fatal("no --accent token")
	}
	if r := contrastRatio(bg[0], accent[0]); r < 4.5 {
		t.Errorf("--bg on --accent in light = %.2f:1, want at least 4.5:1", r)
	}
	if r := contrastRatio(bg[1], accent[1]); r < 4.5 {
		t.Errorf("--bg on --accent in dark = %.2f:1, want at least 4.5:1", r)
	}
}

// The colour maths above is only as good as its agreement with WCAG's own reference values.
func TestContrastRatioMatchesWCAGReferenceValues(t *testing.T) {
	if r := contrastRatio(rgb{0, 0, 0}, rgb{255, 255, 255}); math.Abs(r-21) > 0.01 {
		t.Errorf("black on white = %.2f:1, want 21:1", r)
	}
	// #767676 is the lightest grey that passes 4.5:1 on white.
	if r := contrastRatio(rgb{0x76, 0x76, 0x76}, rgb{255, 255, 255}); math.Abs(r-4.54) > 0.01 {
		t.Errorf("#767676 on white = %.2f:1, want 4.54:1", r)
	}
	if got := mixSRGB(rgb{0, 0, 0}, rgb{255, 255, 255}, 0.5); got != (rgb{127.5, 127.5, 127.5}) {
		//nolint:misspell // color-mix is the CSS function's own name, not prose to be normalised
		t.Errorf("color-mix(in srgb, black 50%%, white) = %+v, want 127.5 per channel", got)
	}

	// Achromatic (grey) pins cannot distinguish permutations of the channel weights: they sum to 1,
	// so any permutation gives the same result when R=G=B. Test each weight independently using
	// pure primaries where linear(255) = 1 and linear(0) = 0, so each channel's luminance is
	// exactly its WCAG coefficient. These are derivable from the specification.
	if got := relativeLuminance(rgb{255, 0, 0}); math.Abs(got-0.2126) > 1e-9 {
		t.Errorf("red channel weight = %.10f, want 0.2126000000", got)
	}
	if got := relativeLuminance(rgb{0, 255, 0}); math.Abs(got-0.7152) > 1e-9 {
		t.Errorf("green channel weight = %.10f, want 0.7152000000", got)
	}
	if got := relativeLuminance(rgb{0, 0, 255}); math.Abs(got-0.0722) > 1e-9 {
		t.Errorf("blue channel weight = %.10f, want 0.0722000000", got)
	}

	// The piecewise branch of the sRGB transfer function (if s <= 0.04045) is linear, never
	// exercised by the grey pins above. At v=1, the piecewise branch gives 1/255/12.92 =
	// 0.000303528 where a pow-only implementation gives 0.000983680 (factor of 3.2).
	if got := relativeLuminance(rgb{1, 1, 1}); math.Abs(got-0.000303528) > 1e-8 {
		t.Errorf("near-black luminance = %.9f, want 0.000303528", got)
	}
}

// waitingStatusBody returns the declaration block of `.waiting-status { ... }` in css, or "" when
// app.css defines no such rule. Tolerant of whitespace, like hueClassBody.
func waitingStatusBody(t *testing.T, css string) string {
	t.Helper()
	m := regexp.MustCompile(`\.waiting-status\s*\{([^}]*)\}`).FindStringSubmatch(css)
	if m == nil {
		return ""
	}
	return m[1]
}

// rgb is a colour's gamma-encoded sRGB channels, 0 to 255.
type rgb struct{ r, g, b float64 }

const hexColour = `#([0-9a-fA-F]{6})`

// hexToken reads `--name: #rrggbb;` from css. On a miss it reports and returns false rather than
// aborting the test, so a stylesheet missing several tokens is reported in full rather than one at
// a time.
func hexToken(t *testing.T, css, name string) (rgb, bool) {
	t.Helper()
	m := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*` + hexColour + `\s*;`).FindStringSubmatch(css)
	if m == nil {
		t.Errorf("app.css declares no --%s: #rrggbb;", name)
		return rgb{}, false
	}
	return parseHexColour(m[1]), true
}

// lightDarkToken reads `--name: light-dark(#light, #dark)` from css: the light and the dark value.
// On a miss it reports and returns false rather than aborting the test.
func lightDarkToken(t *testing.T, css, name string) ([2]rgb, bool) {
	t.Helper()
	m := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*light-dark\(\s*` + hexColour + `\s*,\s*` + hexColour + `\s*\)`).FindStringSubmatch(css)
	if m == nil {
		t.Errorf("app.css declares no --%s: light-dark(#rrggbb, #rrggbb)", name)
		return [2]rgb{}, false
	}
	return [2]rgb{parseHexColour(m[1]), parseHexColour(m[2])}, true
}

// percentToken reads `--name: 7%;` from css as a fraction. On a miss it reports and returns false
// rather than aborting the test.
func percentToken(t *testing.T, css, name string) (float64, bool) {
	t.Helper()
	m := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*([0-9.]+)%\s*;`).FindStringSubmatch(css)
	if m == nil {
		t.Errorf("app.css declares no --%s: N%%;", name)
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Errorf("--%s: %v", name, err)
		return 0, false
	}
	return v / 100, true
}

func parseHexColour(h string) rgb {
	n, _ := strconv.ParseUint(h, 16, 32) // the regexp admits six hex digits only
	return rgb{float64(n >> 16 & 0xff), float64(n >> 8 & 0xff), float64(n & 0xff)}
}

// mixSRGB is CSS `color-mix(in srgb, a p, b)`: a linear interpolation of the gamma-encoded
// channels, p of a and the rest of b.
//
//nolint:misspell // color-mix is the CSS function's own name, not prose to be normalised
func mixSRGB(a, b rgb, p float64) rgb {
	return rgb{a.r*p + b.r*(1-p), a.g*p + b.g*(1-p), a.b*p + b.b*(1-p)}
}

// contrastRatio is the WCAG 2 contrast ratio of two colours, from 1 to 21.
func contrastRatio(a, b rgb) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// relativeLuminance is WCAG 2's relative luminance of an sRGB colour.
func relativeLuminance(c rgb) float64 {
	linear := func(v float64) float64 {
		s := v / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(c.r) + 0.7152*linear(c.g) + 0.0722*linear(c.b)
}

// FR-1.9 AC4: the wait page's status line is text on the page background, and it must be readable
// in both appearances — it is the one signal that is not colour or motion.
func TestWaitingStatusKeepsTextReadable(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	body := waitingStatusBody(t, css)
	if body == "" {
		t.Fatal("app.css defines no .waiting-status rule")
	}
	// The measurement below is only true of the page if .waiting-status actually paints its text
	// from --text; without this the contrast pinned here could belong to a colour the rule never
	// uses (the same seam TestHueClassBindsItsOwnTokens closes for the tile hues).
	//nolint:misspell // color: is the CSS property's own name, not prose to be normalised
	decl := regexp.MustCompile(`color:\s*([^;]+);`).FindStringSubmatch(body)
	if decl == nil {
		t.Fatal(".waiting-status declares no color:") //nolint:misspell // the CSS property's name
	}
	if got := strings.TrimSpace(decl[1]); got != "var(--text)" {
		t.Errorf(".waiting-status color = %q, want var(--text)", got) //nolint:misspell // ditto
	}
	bg, ok := lightDarkToken(t, css, "bg")
	if !ok {
		t.Fatal("no --bg token")
	}
	text, ok := lightDarkToken(t, css, "text")
	if !ok {
		t.Fatal("no --text token")
	}
	for i, name := range []string{"light", "dark"} {
		if r := contrastRatio(text[i], bg[i]); r < 4.5 {
			t.Errorf("--text on --bg (%s) = %.2f:1, want at least 4.5:1", name, r)
		}
	}
}

// FR-1.10 AC3: chip text is the label colour on a chip tinted 10 % with the same colour over the
// page; it must reach 4.5:1 in both appearances, and every key the page can emit must have a
// token — a key without one would draw a chip in the inherited colour and nobody would notice.
func TestLabelChipsKeepTextReadable(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	bg, ok := lightDarkToken(t, css, "bg")
	if !ok {
		t.Fatal("no --bg token")
	}
	for _, key := range labelKeys {
		if !strings.Contains(css, ".label-"+key+" {") {
			t.Errorf("app.css defines no .label-%s rule", key)
		}
		colour, ok := lightDarkToken(t, css, "label-"+key)
		if !ok {
			continue
		}
		for i, name := range []string{"light", "dark"} {
			chip := mixSRGB(colour[i], bg[i], 0.10)
			if r := contrastRatio(colour[i], chip); r < 4.5 {
				t.Errorf("--label-%s on its chip (%s) = %.2f:1, want at least 4.5:1", key, name, r)
			}
		}
	}

	// The rule above only checks each key's own token; it says nothing about which chips actually
	// get tinted. Find the rule whose declaration block carries the tint itself, and check its
	// selector list names every labelKeys entry — a key missing from it would draw in the
	// inherited muted colour instead, and label-other must stay untinted, since it carries no
	// colour of its own to measure.
	//nolint:misspell // color-mix is the CSS function's own name, not prose to be normalised
	tintRule := regexp.MustCompile(`(?s)([^{}]+)\{[^{}]*background:\s*color-mix\(in srgb, var\(--label\) 10%[^{}]*\}`).FindStringSubmatch(css)
	if tintRule == nil {
		t.Fatal("app.css defines no rule tinting a chip's background with color-mix(in srgb, var(--label) 10%, ...)") //nolint:misspell // ditto
	}
	selector := tintRule[1]
	for _, key := range labelKeys {
		if !strings.Contains(selector, ".label-"+key) {
			t.Errorf("the tinted selector list does not include .label-%s: %q", key, selector)
		}
	}
	if strings.Contains(selector, ".label-"+labelOther) {
		t.Errorf("the tinted selector list includes .label-%s, which must keep the plain outline: %q", labelOther, selector)
	}
}

// FR-1.10 AC2: every site's stripe is visible against the page in both appearances — the hue in
// light, the signal colour lightened 30 % toward white in dark, at least 3:1 (the bar for a
// non-text element). Navy and plum as drawn on a tile are nearly invisible as a 4 px line on the
// dark page; the lightening is what this test guards. When a stripe fails, raise the mix
// percentage in app.css and here together; never change a brand colour.
func TestGroupStripesAreVisibleInBothAppearances(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	//nolint:misspell // color-mix is a CSS standard function, not a misspelling
	if !strings.Contains(css, "color-mix(in srgb, var(--tile-sig, var(--hue-slate-sig)) 70%, #ffffff)") {
		t.Fatal("the dark stripe is not the signal colour mixed 30 % toward white; this test measures that mix")
	}
	bg, ok := lightDarkToken(t, css, "bg")
	if !ok {
		t.Fatal("no --bg token")
	}
	white := rgb{255, 255, 255}
	for _, key := range config.HueKeys {
		hue, ok := hexToken(t, css, "hue-"+key)
		if !ok {
			continue
		}
		sig, ok := hexToken(t, css, "hue-"+key+"-sig")
		if !ok {
			continue
		}
		if r := contrastRatio(hue, bg[0]); r < 3 {
			t.Errorf("the %s stripe on the light page = %.2f:1, want at least 3:1", key, r)
		}
		if r := contrastRatio(mixSRGB(sig, white, 0.70), bg[1]); r < 3 {
			t.Errorf("the %s stripe on the dark page = %.2f:1, want at least 3:1", key, r)
		}
	}
}

// FR-1.13 with FR-1.8 AC5: the Security tile's rule is a signal drawn in colour, so it needs the
// 3:1 against the surface behind it that WCAG asks of a non-text indicator — in both appearances,
// and it is the one thing on that tile the eye is meant to catch across the page. The word
// "Security" in the heading carries the meaning either way; the rule only makes it loud.
func TestTheSecurityTilesRuleIsVisibleInBothAppearances(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	if !strings.Contains(css, "inset 4px 0 0 var(--danger)") {
		t.Fatal("the alert tile's rule is not 4 px of --danger; this test measures that colour")
	}
	danger, ok := lightDarkToken(t, css, "danger")
	if !ok {
		t.Fatal("no --danger token")
	}
	surface, ok := lightDarkToken(t, css, "surface")
	if !ok {
		t.Fatal("no --surface token")
	}
	for i, appearance := range []string{"light", "dark"} {
		if r := contrastRatio(danger[i], surface[i]); r < 3 {
			t.Errorf("the security tile's rule on the %s page = %.2f:1, want at least 3:1", appearance, r)
		}
	}
}

// FR-1.8 AC5 covers every tile, including the one that carries no site colour. The Security
// tile's heading is hazard tape — two deep reds, diagonally striped — with white written on it,
// so the word has to stay readable over *both* stripes. The band is one fixed pair of colours in
// both appearances, like the rainbow band: it is a warning, not a theme.
func TestTheSecurityTilesStripedHeadingKeepsTextReadable(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	if !strings.Contains(css, ".tile-tier .tile-title") {
		t.Fatal("app.css has no .tile-tier .tile-title rule")
	}
	//nolint:misspell // repeating-linear-gradient is the CSS function's own name
	if !strings.Contains(css, "repeating-linear-gradient(45deg, var(--tape-red)") {
		t.Fatal("the security tile's heading is not hazard tape; this test measures that band")
	}
	white := rgb{255, 255, 255}
	for _, name := range []string{"tape-red", "tape-red-dark"} {
		stripe, ok := hexToken(t, css, name)
		if !ok {
			continue
		}
		if r := contrastRatio(white, stripe); r < 4.5 {
			t.Errorf("white on --%s = %.2f:1, want at least 4.5:1", name, r)
		}
	}
}

// FR-1.13 AC2: a marked row carries hazard tape down its edge, and the chip is solid rather than
// outlined. Both ends of that have to stay readable: the tape against the page, and the chip's
// word on its own fill, in both appearances.
func TestTheMarkedRowsTapeAndChipKeepTheirContrast(t *testing.T) {
	raw, err := fs.ReadFile(embedded, "static/app.css")
	if err != nil {
		t.Fatalf("reading the embedded app.css: %v", err)
	}
	css := string(raw)
	surface, ok := lightDarkToken(t, css, "surface")
	if !ok {
		t.Fatal("no --surface token")
	}
	// The tape's two stripes are the tier's colour and that colour mixed halfway into the page,
	// so the tier's own colour is the strongest thing in it: measuring that one measures the tape.
	for tier, token := range map[string]string{"security": "danger", "dependency": "warn"} {
		colour, ok := lightDarkToken(t, css, token)
		if !ok {
			continue
		}
		ink, ok := lightDarkToken(t, css, "tier-ink-"+tier)
		if !ok {
			continue
		}
		for i, appearance := range []string{"light", "dark"} {
			if r := contrastRatio(colour[i], surface[i]); r < 3 {
				t.Errorf("the %s tape on the %s page = %.2f:1, want at least 3:1", tier, appearance, r)
			}
			if r := contrastRatio(ink[i], colour[i]); r < 4.5 {
				t.Errorf("the %s chip's word on the %s page = %.2f:1, want at least 4.5:1", tier, appearance, r)
			}
		}
	}
}
