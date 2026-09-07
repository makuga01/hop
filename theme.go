package main

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

type palette struct {
	background, panel, border, ink, muted, accent, focus, focusInk, folder, marked uint32
}

var themes = map[string]palette{
	"lagoon":     {0x063537, 0x104548, 0x4e9896, 0xe7fff5, 0xa3d2c8, 0xffa888, 0x9aefd3, 0x073b36, 0xa5f0cd, 0xffdc93},
	"cobalt":     {0x10164b, 0x1c2463, 0x6675d4, 0xf1f3ff, 0xafbae8, 0xecf76c, 0x3c51d1, 0xffffff, 0x86e4ff, 0xecf76c},
	"afterhours": {0x291632, 0x3b2146, 0xa473ae, 0xfff1fb, 0xd7b6dc, 0xffb080, 0xab386e, 0xffffff, 0xd3b5ff, 0xffdd87},
}

var activeTheme = "lagoon"

var uiBase, uiBorder, uiHeader, uiSelected, uiMarked, uiMuted, uiError, uiFolder, uiAccent, uiPanel, uiKey, uiLogo string

func init() { _ = applyTheme("lagoon") }

func themeName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if _, ok := themes[name]; !ok {
		return "", fmt.Errorf("unknown theme %q; choose lagoon, cobalt, or afterhours", name)
	}
	return name, nil
}

func rgbStyle(fg, bg uint32) string {
	if terminalColorMode() == "256" {
		return fmt.Sprintf("38;5;%d;48;5;%d", indexedColor(fg), indexedColor(bg))
	}
	return fmt.Sprintf("38;2;%d;%d;%d;48;2;%d;%d;%d", fg>>16, fg>>8&255, fg&255, bg>>16, bg>>8&255, bg&255)
}

func terminalColorMode() string {
	mode := os.Getenv("HOP_COLOR_MODE")
	if mode == "" {
		mode = os.Getenv("HOPS_COLOR_MODE")
	}
	if mode == "256" || mode == "truecolor" {
		return mode
	}
	if color := strings.ToLower(os.Getenv("COLORTERM")); color == "truecolor" || color == "24bit" {
		return "truecolor"
	}
	if os.Getenv("TERM_PROGRAM") == "Apple_Terminal" {
		return "256"
	}
	return "truecolor"
}

// Keep colored surfaces colored in the 256-color fallback. Unrestricted nearest
// RGB matching turns deep teal and plum into gray because the gray ramp is finer.
func indexedColor(color uint32) int {
	r, g, b := int(color>>16), int(color>>8&255), int(color&255)
	best, distance := 16, int(^uint(0)>>1)
	levels := []int{0, 95, 135, 175, 215, 255}
	for ri, rv := range levels {
		for gi, gv := range levels {
			for bi, bv := range levels {
				d := (r-rv)*(r-rv) + (g-gv)*(g-gv) + (b-bv)*(b-bv)
				if d < distance {
					best, distance = 16+36*ri+6*gi+bi, d
				}
			}
		}
	}
	if max(r, g, b)-min(r, g, b) < 20 {
		for i := 0; i < 24; i++ {
			v := 8 + 10*i
			d := (r-v)*(r-v) + (g-v)*(g-v) + (b-v)*(b-v)
			if d < distance {
				best, distance = 232+i, d
			}
		}
	}
	return best
}

// Apply once before starting any UI workers. Palettes match the web previews.
func applyTheme(name string) error {
	name, err := themeName(name)
	if err != nil {
		return err
	}
	activeTheme = name
	p := themes[name]
	uiBase = rgbStyle(p.ink, p.background)
	uiBorder = rgbStyle(p.border, p.background)
	uiHeader = rgbStyle(p.muted, p.panel)
	uiPanel = rgbStyle(p.ink, p.panel)
	uiKey = rgbStyle(p.accent, p.panel)
	uiLogo = "1;" + rgbStyle(p.ink, p.panel)
	uiSelected = rgbStyle(p.focusInk, p.focus)
	uiMarked = rgbStyle(p.marked, p.background)
	uiMuted = rgbStyle(p.muted, p.background)
	uiError = "1;" + rgbStyle(0xff9b9b, p.background)
	uiFolder = rgbStyle(p.folder, p.background)
	uiAccent = rgbStyle(p.accent, p.background)
	return nil
}

func cellWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200d || r >= 0xfe00 && r <= 0xfe0f {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe19 || r >= 0xfe30 && r <= 0xfe6f || r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 || r >= 0x1f300 && r <= 0x1faff || r >= 0x20000) {
		return 2
	}
	return 1
}
func textWidth(s string) int {
	n := 0
	for _, r := range s {
		n += cellWidth(r)
	}
	return n
}
func fitRight(s string, n int) string {
	return strings.Repeat(" ", max(0, n-textWidth(s))) + strings.TrimRight(fit(s, n), " ")
}
func fit(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = safeText(s)
	if textWidth(s) <= n {
		return s + strings.Repeat(" ", n-textWidth(s))
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := cellWidth(r)
		if used+w > n-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	b.WriteRune('…')
	return b.String() + strings.Repeat(" ", max(0, n-used-1))
}

// Preserve the filename when a selected path is longer than the box.
func fitEnd(s string, n int) string {
	s = safeText(s)
	if n <= 0 || textWidth(s) <= n {
		return fit(s, n)
	}
	runes := []rune(s)
	used, start := 0, len(runes)
	for start > 0 {
		width := cellWidth(runes[start-1])
		if used+width > n-1 {
			break
		}
		used += width
		start--
	}
	return fit("…"+string(runes[start:]), n)
}

func uiPaint(s, style string) string {
	if !colorEnabled() {
		if style == uiSelected {
			return "\x1b[7m" + s + "\x1b[0m"
		}
		return s
	}
	return styled(s, style, true)
}
func uiLine(s string, n int, style string) string { return uiPaint(fit(s, n), style) + "\r\n" }
func uiRule(title string, n int, top bool) string {
	left, right := "├", "┤"
	if top {
		left, right = "┌", "┐"
	}
	middle := strings.Repeat("─", max(0, n-2))
	if title != "" {
		label := strings.TrimRight(fit(" "+title+" ", max(0, n-4)), " ")
		return uiPaint(left+"─", uiBorder) + uiPaint(label, uiMuted) + uiPaint(strings.Repeat("─", max(0, n-3-textWidth(label)))+right, uiBorder) + "\r\n"
	}
	return uiPaint(left+middle+right, uiBorder) + "\r\n"
}

// Shared geometry keeps the drawn frame and mouse coordinates in agreement.
func contentGeometry(width int) (gutter, widthInside int) {
	width = max(20, width-1)
	gutter = 2
	if width < 50 {
		gutter = 1
	}
	widthInside = min(120, width-2*gutter)
	gutter = (width - widthInside) / 2
	return
}

type uiSpan struct{ text, style string }

func uiSpans(n int, background string, spans ...uiSpan) string {
	var out strings.Builder
	for _, span := range spans {
		if n <= 0 {
			break
		}
		length := min(n, textWidth(safeText(span.text)))
		out.WriteString(uiPaint(fit(span.text, length), span.style))
		n -= length
	}
	out.WriteString(uiPaint(strings.Repeat(" ", max(0, n)), background))
	return out.String()
}

func uiTitle(title, host string, width int) string {
	if host == "" {
		host = "LOCAL"
	}
	host = strings.TrimSpace(fit(host, min(24, width/4)))
	left := "  hop"
	room := max(0, width-textWidth(left)-1-textWidth(host)-6)
	return uiSpans(width, uiHeader, uiSpan{left, uiLogo}, uiSpan{"_", uiKey},
		uiSpan{"   " + fit(title, room), uiHeader}, uiSpan{"  " + host + " ", uiPanel})
}

func uiKeys(keys string, width int) string {
	spans := []uiSpan{{"  ", uiHeader}}
	for _, pair := range strings.Split(strings.TrimSpace(keys), "   ") {
		parts := strings.SplitN(strings.TrimSpace(pair), " ", 2)
		spans = append(spans, uiSpan{parts[0], uiKey})
		if len(parts) > 1 {
			spans = append(spans, uiSpan{" " + parts[1], uiHeader})
		}
		spans = append(spans, uiSpan{"   ", uiHeader})
	}
	return uiSpans(width, uiHeader, spans...)
}

// Paint every cell, including padding, without clearing the screen on redraw.
// The terminal's default background must never leak between themed panels.
func uiScreen(body, title, host, keys string, width, height int) string {
	gutter, inner := contentGeometry(width)
	width = max(20, width-1)
	var out strings.Builder
	out.WriteString("\x1b[H")
	out.WriteString(uiTitle(title, host, width) + "\r\n")
	out.WriteString(uiPaint(strings.Repeat("─", width), uiBorder) + "\r\n")
	lines := strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n")
	for row := 0; row < max(0, height-3); row++ {
		out.WriteString(uiPaint(strings.Repeat(" ", gutter), uiBase))
		if row < len(lines) {
			out.WriteString(lines[row])
		} else {
			out.WriteString(uiPaint(strings.Repeat(" ", inner), uiBase))
		}
		out.WriteString(uiPaint(strings.Repeat(" ", width-inner-gutter), uiBase) + "\r\n")
	}
	out.WriteString(uiKeys(keys, width))
	return out.String()
}
func uiBoxLine(s string, n int, style string) string {
	return uiPaint("│", uiBorder) + uiPaint(fit(s, max(0, n-2)), style) + uiPaint("│", uiBorder) + "\r\n"
}
func uiBottom(n int) string {
	return uiPaint("└"+strings.Repeat("─", max(0, n-2))+"┘", uiBorder) + "\r\n"
}
