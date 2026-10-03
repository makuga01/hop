package main

import (
	"fmt"
	"strings"
	"time"
)

func (d *dualManager) normalKey(key string) string {
	if time.Since(d.prefixAt) > time.Second {
		d.prefix = ""
	}
	previous := d.prefix
	d.prefix = ""
	switch key {
	case "text:o":
		return "settings"
	case "text:/":
		return "filter"
	case "text:h", "text:?":
		return "help"
	case "text:H", "text:u":
		return "left"
	case "text:j":
		return "down"
	case "text:k":
		return "up"
	case "text:l":
		return "right"
	case "text:c", "text:y":
		return "copy"
	case "text:m":
		return "move"
	case "text:D":
		return "delete"
	case "text:d":
		if previous == "d" {
			return "delete"
		}
		d.prefix = "d"
		d.prefixAt = time.Now()
		return ""
	case "text:g":
		if previous == "g" {
			return "home"
		}
		d.prefix = "g"
		d.prefixAt = time.Now()
		return ""
	case "text:G":
		return "end"
	case "text:n":
		return "newfolder"
	case "text:b":
		return "machine"
	case "text:r":
		return "recent"
	case "text:R":
		return "refresh"
	case "text:s":
		return "sort"
	case "text:.":
		return "hidden"
	case "text:P":
		return "path"
	case "text:q":
		return "quit"
	}
	if strings.HasPrefix(key, "text:") {
		return ""
	}
	return key
}

// Filter text is interpreted only while the filter bar has focus.
func (d *dualManager) filterKey(key string) bool {
	m := d.panes[d.active]
	switch key {
	case "esc", "enter":
		d.filtering[d.active] = false
	case "clear":
		m.query = ""
		m.cursor = 0
	case "backspace", "hidden":
		r := []rune(m.query)
		if len(r) > 0 {
			m.query = string(r[:len(r)-1])
		}
		m.cursor = 0
	case "space":
		m.query += " "
		m.cursor = 0
	default:
		if strings.HasPrefix(key, "text:") {
			m.query += key[5:]
			m.cursor = 0
		} else {
			return false
		}
	}
	return true
}

var managerCommands = [][2]string{
	{"o / Options button", "Session and SSH settings"},
	{"r (after an error)", "Retry the same operation"},
	{"←/→ · Tab (dialog)", "Choose an action; highlighted button is selected"},
	{"Enter / Esc (dialog)", "Activate selected action / cancel"},
	{"/ · click Filter", "Focus filter; type to match names"},
	{"Esc / Enter (filter)", "Leave filter; keep matching files"},
	{"Esc (normal mode)", "Deselect all; then clear filter/status"},
	{"Ctrl-U", "Clear filter"},
	{"j / k · Up / Down", "Move cursor down / up"},
	{"H / u · Left / Backspace", "Parent folder"},
	{"l / Right · Enter", "Open folder; Enter marks a file"},
	{"gg / G · Home / End", "First / last entry"},
	{"PgUp / PgDn · wheel", "Page / move active-panel cursor"},
	{"Tab · click panel", "Switch only; keeps cursor and marks"},
	{"Space · right click", "Mark/unmark without advancing"},
	{"c / y · Ctrl-S / Ctrl-T", "Copy to opposite panel"},
	{"m", "Move to opposite panel; confirms"},
	{"dd / D", "Delete selection; always confirms"},
	{"Enter (confirmation)", "Perform operation; Esc cancels"},
	{"n / Ctrl-N", "Create and open folder"},
	{"P / Ctrl-L", "Edit current path"},
	{"b / Ctrl-B", "Choose SSH machine"},
	{"r / Ctrl-R", "Recent folders for active panel"},
	{"R / Ctrl-E", "Refresh both panels"},
	{". / Ctrl-H", "Toggle hidden files"},
	{"s / Ctrl-O", "Cycle name/date/size/type sort"},
	{"Click column / Sort", "Sort column / cycle sorting"},
	{"Click / double-click", "Move cursor / open folder"},
	{"Click/drag scrollbar", "Scroll active pane"},
	{"h / ?", "Show this command table"},
	{"q / Ctrl-C", "Quit; Ctrl-C cancels active work"},
}

func (d *dualManager) helpView(w, h int) string {
	_, inner := contentGeometry(w)
	rows := max(1, h-7)
	d.helpOffset = max(0, min(d.helpOffset, max(0, len(managerCommands)-rows)))
	var out strings.Builder
	out.WriteString(uiRule("Commands · normal mode", inner, true))
	for _, c := range managerCommands[d.helpOffset:min(len(managerCommands), d.helpOffset+rows)] {
		out.WriteString(uiBoxLine(" "+fit(c[0], min(28, inner/2))+"  "+c[1], inner, uiBase))
	}
	out.WriteString(uiBottom(inner))
	return uiScreen(out.String(), fmt.Sprintf("Help · %d–%d of %d", d.helpOffset+1, min(len(managerCommands), d.helpOffset+rows), len(managerCommands)), d.host.Label(), "j/k Scroll   Esc / h / ? Close", w, h)
}
