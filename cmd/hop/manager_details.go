package main

import (
	"fmt"
	"strings"
)

// Wrap by terminal cells, including long paths with no spaces. Error details
// remain available in a scrollable view instead of being cut off in the dock.
func detailLines(message string, width int) []string {
	width = max(1, width)
	var lines []string
	for _, paragraph := range strings.Split(message, "\n") {
		line, cells := "", 0
		for _, r := range safeText(paragraph) {
			n := textWidth(string(r))
			if cells+n > width && line != "" {
				if at := strings.LastIndexByte(line, ' '); at > 0 {
					lines = append(lines, line[:at])
					line = line[at+1:]
					cells = textWidth(line)
				} else {
					lines = append(lines, line)
					line, cells = "", 0
				}
			}
			line += string(r)
			cells += n
		}
		lines = append(lines, line)
	}
	return lines
}
func (d *dualManager) detailsView(w, h int) string {
	_, width := contentGeometry(w)
	lines := detailLines(d.details, width-4)
	rows := max(1, h-7)
	d.detailOffset = max(0, min(d.detailOffset, len(lines)-rows))
	end := min(len(lines), d.detailOffset+rows)
	var body strings.Builder
	body.WriteString(uiRule("Operation details", width, true))
	for _, line := range lines[d.detailOffset:end] {
		body.WriteString(uiBoxLine(" "+line, width, uiBase))
	}
	body.WriteString(uiBottom(width))
	return uiScreen(body.String(), fmt.Sprintf("Details · %d–%d of %d", d.detailOffset+1, end, len(lines)), d.host.Label(), "↑/↓ Scroll   PgUp/PgDn Page   Esc Back", w, h)
}
func (d *dualManager) detailsKey(key string, h int) {
	if e, ok := parseMouse(key); ok && !e.release {
		if e.button == 64 {
			key = "up"
		}
		if e.button == 65 {
			key = "down"
		}
	}
	switch key {
	case "esc":
		d.details = ""
	case "up", "text:k":
		d.detailOffset--
	case "down", "text:j":
		d.detailOffset++
	case "pageup":
		d.detailOffset -= max(1, h-7)
	case "pagedown":
		d.detailOffset += max(1, h-7)
	case "home":
		d.detailOffset = 0
	case "end":
		d.detailOffset = len(d.details)
	}
}
