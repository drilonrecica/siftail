package ui

import (
	"bytes"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Tests for the signature items of DESIGN.md §§12.3–12.5, 27–28, and 31
// (ADR 0011 D3, D4, D8–D10, D12, D13).

func TestMessageSegmentsMatchLikeTheContainsFilter(t *testing.T) {
	type seg = MessageSegment
	for _, tc := range []struct {
		name, message, contains string
		want                    []MessageSegment
	}{
		{"no filter", "Hello", "", []seg{{Text: "Hello"}}},
		{"no match", "Hello", "xyz", []seg{{Text: "Hello"}}},
		{"ascii fold both ways", "Error: ERROR error", "eRRor",
			[]seg{{Text: "Error", Match: true}, {Text: ": "}, {Text: "ERROR", Match: true}, {Text: " "}, {Text: "error", Match: true}}},
		{"non-overlapping left to right", "aaaa", "aa",
			[]seg{{Text: "aa", Match: true}, {Text: "aa", Match: true}}},
		{"whole message", "needle", "NEEDLE", []seg{{Text: "needle", Match: true}}},
		// SQLite lower() folds ASCII only, so É never matches é.
		{"non-ascii is literal", "Énorme énorme", "énorme",
			[]seg{{Text: "Énorme "}, {Text: "énorme", Match: true}}},
		{"multibyte neighbours", "日本ERR語", "err",
			[]seg{{Text: "日本"}, {Text: "ERR", Match: true}, {Text: "語"}}},
		{"invalid utf-8 keeps offsets", "\xffABC\xfe", "b",
			[]seg{{Text: "\xffA"}, {Text: "B", Match: true}, {Text: "C\xfe"}}},
		{"hostile markup is just text", "<mark>x</mark><script>alert(1)</script>", "<script>",
			[]seg{{Text: "<mark>x</mark>"}, {Text: "<script>", Match: true}, {Text: "alert(1)</script>"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MessageSegments(tc.message, tc.contains)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("MessageSegments(%q, %q) = %#v, want %#v", tc.message, tc.contains, got, tc.want)
			}
			var joined strings.Builder
			for _, segment := range got {
				joined.WriteString(segment.Text)
			}
			if joined.String() != tc.message {
				t.Fatalf("segments rejoin to %q, want %q", joined.String(), tc.message)
			}
		})
	}
}

func TestMessageExtraLinesIgnoresTrailingBreaks(t *testing.T) {
	for message, want := range map[string]int64{
		"":                  0,
		"one":               0,
		"one\n":             0,
		"one\r\n\r\n":       0,
		"one\ntwo":          1,
		"one\r\ntwo\nthree": 2,
		"\nleading":         1,
	} {
		if got := MessageExtraLines(message); got != want {
			t.Errorf("MessageExtraLines(%q) = %d, want %d", message, got, want)
		}
	}
}

func TestHistoryRowEscapesHostileContentAndMarksMatches(t *testing.T) {
	renderer := New()
	message := `<img src=x onerror=alert(1)> NEEDLE "quoted" & <mark>fake</mark> needle`
	row := HistoryRowView{
		ID: 7, DetailID: "event-detail-7", DetailURL: "/logs/events/7",
		TimestampUTC: "2026-11-19T10:00:00Z", Timestamp: "10:00:00.000",
		Level: "fatal", Stream: "stderr", Source: "<b>app</b>/web",
		Message:         message,
		MessageSegments: MessageSegments(message, "needle"),
		ExtraLines:      18,
		ToggleLabel:     RowToggleLabel("fatal", "<b>app</b>/web", message),
	}
	var out bytes.Buffer
	if err := renderer.templates.ExecuteTemplate(&out, "history-row", row); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	for _, forbidden := range []string{"<img", "<b>", "<mark>fake", `"quoted"`} {
		if strings.Contains(html, forbidden) {
			t.Errorf("row rendered hostile content unescaped: %q in\n%s", forbidden, html)
		}
	}
	for _, want := range []string{
		`class="log-row level-fatal"`,
		`<span class="severity severity-fatal">FTL</span>`,
		`&lt;img src=x onerror=alert(1)&gt; <mark>NEEDLE</mark> &#34;quoted&#34; &amp; &lt;mark&gt;fake&lt;/mark&gt; <mark>needle</mark>`,
		`<span class="row-lines">+18 lines</span>`,
		`<svg class="icon" aria-hidden="true" focusable="false"><use href="#icon-chevron"/></svg>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("row missing %q in\n%s", want, html)
		}
	}
	if got := strings.Count(html, "<mark>"); got != 2 {
		t.Errorf("row has %d <mark> elements, want 2", got)
	}

	row.ExtraLines = 1
	row.MessageSegments = MessageSegments("plain", "")
	out.Reset()
	if err := renderer.templates.ExecuteTemplate(&out, "history-row", row); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `<span class="row-lines">+1 line</span>`) ||
		strings.Contains(out.String(), "<mark>") {
		t.Errorf("single extra line or unfiltered row rendered wrong:\n%s", out.String())
	}
}

// TestIconSpriteHasTheSpecifiedSymbols keeps the sprite to the set of
// DESIGN.md §31 and checks the shell renders it once.
func TestIconSpriteHasTheSpecifiedSymbols(t *testing.T) {
	var out bytes.Buffer
	if err := New().templates.ExecuteTemplate(&out, "icon-sprite.html", nil); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`<symbol id="icon-([a-z-]+)" viewBox="0 0 16 16">`).FindAllStringSubmatch(out.String(), -1)
	var names []string
	for _, id := range ids {
		names = append(names, id[1])
	}
	want := []string{"chevron", "copy", "check", "close", "search", "external", "arrow-down", "menu"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("sprite symbols = %v, want %v", names, want)
	}
	shell, err := files.ReadFile("templates/shell.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(shell), `{{template "icon-sprite.html"}}`) != 1 {
		t.Fatal("shell must render the icon sprite exactly once")
	}
}

// TestNoUnicodeGlyphIcons covers ADR 0011 D4: chevrons, arrows, and menu
// marks come from the sprite. ⌘ inside <kbd> is text, not an icon.
func TestNoUnicodeGlyphIcons(t *testing.T) {
	glyphs := []string{"›", "‹", "⌄", "▾", "▸", "↓", "↑", "←", "→", "✓", "✕", "×", "☰"}
	check := func(name, text string) {
		for _, glyph := range glyphs {
			if strings.Contains(text, glyph) {
				t.Errorf("%s uses the glyph %q as an icon", name, glyph)
			}
		}
	}
	entries, err := files.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		content, err := files.ReadFile("templates/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		check(entry.Name(), string(content))
	}
	for _, name := range []string{"app.js", "live.js", "palette.js", "keyboard.js", "app.css"} {
		content, err := files.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		// Comments may describe arrows in prose; check code only.
		code := regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`).ReplaceAllString(string(content), "")
		check(name, code)
	}
}

// TestTypeScaleUsesTokensOnly covers DESIGN.md §28.2: every font-size in
// app.css is a scale token, the scale has no step below 12 px, and the
// monospace face ships only 400 and 700 static files (§28.1).
func TestTypeScaleUsesTokensOnly(t *testing.T) {
	appCSS, err := files.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	allowed := regexp.MustCompile(`^var\(--text-(xs|sm|md|lg|xl|2xl|log)\)$|^inherit$`)
	for _, m := range regexp.MustCompile(`font-size:\s*([^;]+);`).FindAllStringSubmatch(string(appCSS), -1) {
		if !allowed.MatchString(strings.TrimSpace(m[1])) {
			t.Errorf("app.css font-size %q is not a scale token", m[1])
		}
	}
	tokensCSS, err := files.ReadFile("assets/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	tokens := string(tokensCSS)
	if strings.Contains(tokens, "--text-2xs") || strings.Contains(string(appCSS), "--text-2xs") {
		t.Error("--text-2xs is removed from the scale (ADR 0011 D12)")
	}
	// --text-xs at the 15 px Compact root must be at least 12 px.
	xs := regexp.MustCompile(`--text-xs:\s*([0-9.]+)rem;`).FindStringSubmatch(tokens)
	if xs == nil {
		t.Fatal("--text-xs missing")
	}
	if xs[1] != "0.8" {
		t.Errorf("--text-xs = %srem; the 12 px floor needs 0.8rem at the 15 px root", xs[1])
	}
	weights := regexp.MustCompile(`font-weight:\s*([0-9 ]+);\s*font-display: swap;\s*src: url\("/assets/(jetbrains-mono-[0-9a-z-]+\.woff2)"\)`).FindAllStringSubmatch(tokens, -1)
	got := map[string]string{}
	for _, w := range weights {
		got[w[2]] = w[1]
	}
	want := map[string]string{
		"jetbrains-mono-400-latin.woff2":     "400",
		"jetbrains-mono-400-latin-ext.woff2": "400",
		"jetbrains-mono-700-latin.woff2":     "700",
		"jetbrains-mono-700-latin-ext.woff2": "700",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("font faces = %v, want %v", got, want)
	}
	for name := range want {
		if _, err := files.ReadFile("assets/" + name); err != nil {
			t.Errorf("font file %s is not embedded: %v", name, err)
		}
	}
}

// TestSeveritySignatureRules covers DESIGN.md §12.3 and §12.5: edges on
// warn, error, and fatal only, fatal wider and filled, info secondary, and
// a keyboard selection distinct from hover.
func TestSeveritySignatureRules(t *testing.T) {
	css, err := files.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	text := string(css)
	for _, want := range []string{
		".log-row.level-warn {\n  --row-edge: inset 2px 0 0 var(--status-warning);",
		".log-row.level-error {\n  --row-edge: inset 2px 0 0 var(--status-error);",
		".log-row.level-fatal {\n  --row-edge: inset 3px 0 0 var(--status-fatal);",
		".severity-info,\n.severity-unknown {\n  color: var(--text-secondary);",
		"  background: var(--status-fatal);\n  color: var(--text-inverse);\n  font-weight: 700;",
		".log-row[data-row-selected],\n.log-row:focus-visible {\n  outline: 2px solid var(--focus-ring);\n  outline-offset: -5px;\n  background: var(--surface-selected);",
		".row-text mark {\n  padding: 0;\n  background: var(--mark-bg);\n  color: inherit;",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("app.css missing %q", want)
		}
	}
	for _, level := range []string{"trace", "debug", "info", "unknown"} {
		if strings.Contains(text, ".log-row.level-"+level+" {") {
			t.Errorf("level %s must not draw an edge", level)
		}
	}
	// Monospace text uses 400 and 700 only: the base severity rule is 400.
	if !strings.Contains(text, ".severity {\n  font-family: var(--font-mono);\n  font-size: var(--text-xs);\n  font-weight: 400;") {
		t.Error("severity codes must be weight 400")
	}
}

// TestMatchHighlightContrast checks the row's message colour on --mark-bg
// in both themes (DESIGN.md §27.3).
func TestMatchHighlightContrast(t *testing.T) {
	tokens := parseThemeTokens(t)
	mark, ok := tokens["mark-bg"]
	if !ok {
		t.Fatal("--mark-bg missing")
	}
	text := tokens["text-primary"]
	if r := contrastRatio(text.dark, mark.dark); r < 4.5 {
		t.Errorf("dark text on mark = %.2f:1", r)
	}
	if r := contrastRatio(text.light, mark.light); r < 4.5 {
		t.Errorf("light text on mark = %.2f:1", r)
	}
	fatal := tokens["status-fatal"]
	if fatal.dark != "#ff6b63" {
		t.Errorf("dark fatal = %s, want #ff6b63 (ADR 0011 D3)", fatal.dark)
	}
}
