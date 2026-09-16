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
	surface := lightDarkToken(t, css, "surface")
	foregrounds := map[string][2]rgb{
		"--text":   lightDarkToken(t, css, "text"),
		"--muted":  lightDarkToken(t, css, "muted"),
		"--accent": lightDarkToken(t, css, "accent"),
	}
	washLight := percentToken(t, css, "tile-wash-light")
	washDark := percentToken(t, css, "tile-wash-dark")
	white := rgb{255, 255, 255}

	for _, key := range config.HueKeys {
		if !strings.Contains(css, ".hue-"+key+" {") {
			t.Errorf("app.css defines no .hue-%s class", key)
		}
		band := hexToken(t, css, "hue-"+key)
		sig := hexToken(t, css, "hue-"+key+"-sig")
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

// rgb is a colour's gamma-encoded sRGB channels, 0 to 255.
type rgb struct{ r, g, b float64 }

const hexColour = `#([0-9a-fA-F]{6})`

// hexToken reads `--name: #rrggbb;` from css.
func hexToken(t *testing.T, css, name string) rgb {
	t.Helper()
	m := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*` + hexColour + `\s*;`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("app.css declares no --%s: #rrggbb;", name)
	}
	return parseHexColour(m[1])
}

// lightDarkToken reads `--name: light-dark(#light, #dark)` from css: the light and the dark value.
func lightDarkToken(t *testing.T, css, name string) [2]rgb {
	t.Helper()
	m := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*light-dark\(\s*` + hexColour + `\s*,\s*` + hexColour + `\s*\)`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("app.css declares no --%s: light-dark(#rrggbb, #rrggbb)", name)
	}
	return [2]rgb{parseHexColour(m[1]), parseHexColour(m[2])}
}

// percentToken reads `--name: 7%;` from css as a fraction.
func percentToken(t *testing.T, css, name string) float64 {
	t.Helper()
	m := regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `:\s*([0-9.]+)%\s*;`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("app.css declares no --%s: N%%;", name)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("--%s: %v", name, err)
	}
	return v / 100
}

func parseHexColour(h string) rgb {
	n, _ := strconv.ParseUint(h, 16, 32) // the regexp admits six hex digits only
	return rgb{float64(n >> 16 & 0xff), float64(n >> 8 & 0xff), float64(n & 0xff)}
}

// mixSRGB is CSS `color-mix(in srgb, a p, b)`: a linear interpolation of the gamma-encoded
// channels, p of a and the rest of b.
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
