package ui

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// themeColors holds one semantic token's light and dark values as declared
// with light-dark() in tokens.css, plus the static dark fallback declared
// before it (DESIGN.md §27.2).
type themeColors struct {
	light, dark, fallback string
}

var (
	lightDarkDecl = regexp.MustCompile(`--([a-z0-9-]+):\s*light-dark\((#[0-9a-fA-F]{6}),\s*(#[0-9a-fA-F]{6})\);`)
	staticDecl    = regexp.MustCompile(`--([a-z0-9-]+):\s*(#[0-9a-fA-F]{6});`)
)

func parseThemeTokens(t *testing.T) map[string]themeColors {
	t.Helper()
	css, err := files.ReadFile("assets/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]themeColors{}
	for _, m := range lightDarkDecl.FindAllStringSubmatch(string(css), -1) {
		if _, dup := tokens[m[1]]; dup {
			t.Fatalf("token --%s is defined with light-dark() more than once", m[1])
		}
		tokens[m[1]] = themeColors{light: strings.ToLower(m[2]), dark: strings.ToLower(m[3])}
	}
	for _, m := range staticDecl.FindAllStringSubmatch(string(css), -1) {
		tc, ok := tokens[m[1]]
		if !ok {
			continue
		}
		tc.fallback = strings.ToLower(m[2])
		tokens[m[1]] = tc
	}
	if len(tokens) == 0 {
		t.Fatal("no light-dark() tokens found in tokens.css")
	}
	return tokens
}

func channel(hex string, i int) float64 {
	v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
	if err != nil {
		panic(err)
	}
	c := float64(v) / 255
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// relativeLuminance and contrastRatio follow the WCAG 2.2 definitions.
func relativeLuminance(hex string) float64 {
	return 0.2126*channel(hex, 0) + 0.7152*channel(hex, 1) + 0.0722*channel(hex, 2)
}

func contrastRatio(a, b string) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestContrastRatioMatchesWCAGReference(t *testing.T) {
	if got := contrastRatio("#000000", "#ffffff"); math.Abs(got-21) > 1e-9 {
		t.Fatalf("black on white = %.4f, want 21", got)
	}
	if got := contrastRatio("#777777", "#ffffff"); math.Abs(got-4.48) > 0.01 {
		t.Fatalf("#777 on white = %.4f, want 4.48", got)
	}
}

// TestTokenContrastPairs verifies the pairings documented in DESIGN.md §27.3
// in both themes: text at 4.5:1 (WCAG 1.4.3) on every surface it is drawn on,
// including the hover and selected row surfaces, and borders, focus rings,
// and filled controls at 3:1 (WCAG 1.4.11).
func TestTokenContrastPairs(t *testing.T) {
	tokens := parseThemeTokens(t)
	allSurfaces := []string{"surface-base", "surface-raised", "surface-overlay", "surface-hover", "surface-selected"}
	type pair struct {
		fg, bg string
		min    float64
	}
	var pairs []pair
	for _, fg := range []string{
		"text-primary", "text-secondary", "text-muted",
		"accent", "accent-hover",
		"status-info", "status-warning", "status-error", "status-fatal", "status-success",
	} {
		for _, bg := range allSurfaces {
			pairs = append(pairs, pair{fg, bg, 4.5})
		}
	}
	for _, fg := range []string{"border-strong"} {
		for _, bg := range []string{"surface-base", "surface-raised"} {
			pairs = append(pairs, pair{fg, bg, 3})
		}
	}
	for _, bg := range allSurfaces {
		pairs = append(pairs, pair{"focus-ring", bg, 3})
	}
	// Filled buttons and chips: inverse text on the fill, and the fill
	// itself against the page.
	for _, fill := range []string{"accent", "accent-hover", "status-error", "status-fatal"} {
		pairs = append(pairs, pair{"text-inverse", fill, 4.5})
		pairs = append(pairs, pair{fill, "surface-base", 3})
	}

	for _, theme := range []string{"dark", "light"} {
		for _, p := range pairs {
			fg, ok := tokens[p.fg]
			if !ok {
				t.Fatalf("token --%s missing from tokens.css", p.fg)
			}
			bg, ok := tokens[p.bg]
			if !ok {
				t.Fatalf("token --%s missing from tokens.css", p.bg)
			}
			a, b := fg.dark, bg.dark
			if theme == "light" {
				a, b = fg.light, bg.light
			}
			ratio := contrastRatio(a, b)
			name := fmt.Sprintf("%s --%s (%s) on --%s (%s)", theme, p.fg, a, p.bg, b)
			if ratio < p.min {
				t.Errorf("%s = %.2f:1, want at least %.1f:1", name, ratio, p.min)
			} else if testing.Verbose() {
				t.Logf("%s = %.2f:1", name, ratio)
			}
		}
	}
}

// TestTokenFallbacksMatchDarkTheme keeps the static fallbacks for browsers
// without light-dark() identical to the dark values (DESIGN.md §27.2).
func TestTokenFallbacksMatchDarkTheme(t *testing.T) {
	for name, tc := range parseThemeTokens(t) {
		if tc.fallback == "" {
			t.Errorf("--%s has no static dark fallback before its light-dark() value", name)
			continue
		}
		if tc.fallback != tc.dark {
			t.Errorf("--%s fallback %s differs from its dark value %s", name, tc.fallback, tc.dark)
		}
	}
}

// TestComponentStylesUseOneMonospaceFaceAndStyleButtonLinks covers audit
// UX-09 and UX-22.
func TestComponentStylesUseOneMonospaceFaceAndStyleButtonLinks(t *testing.T) {
	css, err := files.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	text := string(css)
	if strings.Contains(text, "ui-monospace") {
		t.Error("app.css names a monospace stack directly; use var(--font-mono)")
	}
	for _, want := range []string{
		"button,\n.button,\ninput,\nselect {\n  min-height: 2.5rem;\n  border: 1px solid var(--border-strong);",
		".button {\n  color: var(--text-primary);\n  text-decoration: none;\n}",
		".button:hover {\n  color: var(--text-primary);\n}",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("app.css missing %q", want)
		}
	}
}

// TestNamedContainersHaveARole covers audit UX-26: an aria-label on a plain
// div is ignored by screen readers unless the div has a role.
func TestNamedContainersHaveARole(t *testing.T) {
	entries, err := files.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	divOpen := regexp.MustCompile(`<div\b[^>]*>`)
	for _, entry := range entries {
		content, err := files.ReadFile("templates/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range divOpen.FindAllString(string(content), -1) {
			if strings.Contains(tag, "aria-label") && !strings.Contains(tag, "role=") {
				t.Errorf("%s: %s has aria-label but no role", entry.Name(), tag)
			}
		}
	}
}
