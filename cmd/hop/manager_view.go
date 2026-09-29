package main

import (
	"fmt"
	"strings"
	"time"
)

type dualLayout struct{ width, gutter, left, right, pane, rows, selectionRows, selectionTop int }

func (d *dualManager) layout(w, h int) dualLayout {
	l := dualLayout{width: max(20, w-1), gutter: 2}
	l.left = 3
	l.pane = (l.width - 2*l.gutter - 3) / 2
	l.right = l.left + l.pane + 3
	count := len(d.panes[d.active].marks)
	extra := 0
	if d.transfer != nil {
		extra = min(7, max(4, h-14))
	} else if count > 0 && h >= 20 {
		l.selectionRows = min(3, count)
		extra = l.selectionRows + 3
	}
	l.rows = max(1, h-13-extra)
	l.selectionTop = 14 + l.rows
	return l
}
func dualColumns(w int) (name int, date bool) {
	name = w - 17
	date = w >= 62
	if date {
		name -= 16
	}
	return max(1, name), date
}
func dualSortAt(x, w int) string {
	name, date := dualColumns(w)
	if x < 2 || x >= w {
		return ""
	}
	if x <= name+4 {
		return "name"
	}
	if date && x > name+16 {
		return "date"
	}
	return "size"
}
func (d *dualManager) paneLines(side, w, rows int) []string {
	m := d.panes[side]
	nameWidth, date := dualColumns(w)
	lines := []string{uiRule(d.paneLabel(side), w, true), uiBoxLine(" "+strings.TrimRight(fitEnd(m.current, w-3), " "), w, uiBase)}
	filter := " Filter: " + m.query
	if m.editing {
		filter = " Path: " + m.edit + "_"
		if m.creating {
			filter = " New folder: " + m.edit + "_"
		}
	}
	if m.loading {
		filter = " Loading…"
	}
	control := m.sortControl()
	if !m.editing && !m.loading && w > 32 {
		filter = fit(filter, max(0, w-3-textWidth(control))) + control
	}
	filterStyle := uiHeader
	if d.active == side && !m.loading && (m.editing || d.filtering[side]) {
		filterStyle = uiSelected
		if !m.editing {
			filter = " Filter: " + m.query + "_"
		}
	}
	lines = append(lines, uiBoxLine(filter, w, filterStyle))
	header := "   " + fit(m.sortLabel("name", "Name"), nameWidth) + "  " + fitRight(m.sortLabel("size", "Size"), 10)
	if date {
		header += "  " + fit(m.sortLabel("date", "Modified"), 14)
	}
	lines = append(lines, uiBoxLine(header, w, uiMuted), uiRule("", w, false))
	visible := m.visible()
	cursor, start := m.view(rows)
	for row := 0; row < rows; row++ {
		i := start + row
		line := ""
		style := uiBase
		if i < len(visible) {
			f := visible[i]
			name := f.Name
			mark := " "
			if f.Dir {
				style = uiFolder
				if name != ".." {
					name += "/"
				}
			}
			if _, ok := m.marks[f.Path]; ok {
				style = uiMarked
				mark = "*"
			}
			if i == cursor {
				if d.active == side && !d.filtering[side] && !m.editing {
					style = uiSelected
				} else {
					style = uiPanel
				}
			}
			size := humanSize(f.Size)
			if f.Dir {
				size = "<DIR>"
			} else if !f.Regular {
				size = "<LINK>"
			}
			line = " " + mark + " " + fit(name, nameWidth) + "  " + fitRight(size, 10)
			if date {
				stamp := ""
				if f.Modified > 0 {
					stamp = time.Unix(f.Modified, 0).Format("02 Jan 15:04")
				}
				line += "  " + fit(stamp, 14)
			}
		} else if row == 0 {
			line = " Empty folder"
			if m.query != "" {
				line = " No matching files"
			}
			if m.loading {
				line = " Loading…"
			}
		}
		lines = append(lines, scrollLine(line, w, style, row, start, rows, len(visible)))
	}
	lines = append(lines, uiRule("", w, false), uiBoxLine(fmt.Sprintf(" %d entries · %d marked", len(visible), len(m.marks)), w, uiMuted), uiBottom(w))
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r\n")
	}
	return lines
}
func (d *dualManager) render(w, h int) string {
	if d.settings {
		return d.settingsView(w, h)
	}
	if d.help {
		return d.helpView(w, h)
	}
	l := d.layout(w, h)
	if w < 64 || h < 18 {
		_, inner := contentGeometry(w)
		return uiScreen(uiLine("Resize to at least 64 columns × 18 rows", inner, uiError), "Two-panel SSH", d.host.Label(), "Esc Quit", w, h)
	}
	var out strings.Builder
	out.WriteString("\x1b[H" + uiTitle("LOCAL ↔ REMOTE", d.host.Label(), l.width) + "\r\n")
	direction := "Copy →"
	if d.active == 1 {
		direction = "← Copy"
	}
	out.WriteString(uiSpans(l.width, uiHeader, uiSpan{"  [ Machine ]", uiKey}, uiSpan{"   [ " + direction + " ]", uiKey}, uiSpan{"   [ New folder ]", uiKey}, uiSpan{"   [ Refresh ]", uiHeader}, uiSpan{"   [ Hidden ]", uiKey}, uiSpan{"   [ Options ]", uiKey}) + "\r\n")
	out.WriteString(uiLine("", l.width, uiBase))
	left, right := d.paneLines(0, l.pane, l.rows), d.paneLines(1, l.pane, l.rows)
	for i := range left {
		out.WriteString(uiPaint("  ", uiBase) + left[i] + uiPaint("   ", uiBase) + right[i] + uiPaint(strings.Repeat(" ", max(0, l.width-2*l.pane-5)), uiBase) + "\r\n")
	}
	drawn := 3 + len(left)
	if d.transfer != nil {
		for _, line := range d.transferLines(l.width, min(7, max(4, h-14))) {
			out.WriteString(line)
			drawn++
		}
	} else if l.selectionRows > 0 {
		out.WriteString(uiLine("", l.width, uiBase))
		drawn++
		m := d.panes[d.active]
		items := m.selected()
		start := m.selectionStart(l.selectionRows)
		title := fmt.Sprintf("Selected %s · %d items", []string{"LOCAL", "REMOTE"}[d.active], len(items))
		out.WriteString(uiRule(title, l.width, true))
		drawn++
		for row, f := range items[start:min(len(items), start+l.selectionRows)] {
			name := f.Path
			if f.Dir {
				name += "/"
			}
			out.WriteString(scrollLine(" * "+strings.TrimRight(fitEnd(name, l.width-6), " "), l.width, uiMarked, row, start, l.selectionRows, len(items)))
			drawn++
		}
		out.WriteString(uiBottom(l.width))
		drawn++
	}
	for drawn < h-2 {
		out.WriteString(uiLine("", l.width, uiBase))
		drawn++
	}
	status := d.notice
	if d.transfer != nil && (d.transfer.busy() || d.transfer.stage == "confirm") {
		status = "To: " + d.transfer.destination
		if d.transfer.action.kind == "delete" {
			status = d.transfer.rootSummary()
		}
	}
	style := uiMuted
	if d.panes[d.active].notice != "" {
		status = d.panes[d.active].notice
		style = uiError
	}
	if d.filtering[d.active] {
		status = "Filter mode · Esc keeps the query and returns to file commands"
	}
	if status == "" {
		status = "Space marks in place · Enter opens · Click a column to sort"
	}
	out.WriteString(uiLine(" "+status, l.width, style))
	footer := "Tab Panel   c Copy  m Move  dd Delete  / Filter  h Help  o Options"
	if d.transfer != nil {
		if d.transfer.busy() {
			footer = d.transfer.verb() + " · Ctrl-C Cancel and quit"
		}
		if d.transfer.stage == "conflict" {
			footer = d.transfer.conflictFooter()
		}
		if d.transfer.stage == "error" {
			footer = "r Retry   o Options   Esc Dismiss"
		}
		if d.transfer.stage == "confirm" {
			footer = "Enter " + d.transfer.verb() + " now   Esc Cancel"
		}
	}
	if d.filtering[d.active] {
		footer = "FILTER · Type to match   Esc / Enter Leave   Ctrl-U Clear"
	}
	if m := d.panes[d.active]; m.editing {
		footer = "PATH · Type a path   Enter Open   Esc Cancel"
		if m.creating {
			footer = "NEW FOLDER · Type a name   Enter Create   Esc Cancel"
		}
	}
	if d.prefix != "" {
		footer = d.prefix + "…   " + footer
	}
	out.WriteString(uiKeys(footer, l.width))
	return out.String()
}

// Mouse actions use the same geometry as rendering, including scrollbar drag.
func (d *dualManager) mouse(e mouseEvent, w, h int) string {
	if e.release {
		for _, m := range d.panes {
			m.dragging = 0
		}
		return ""
	}
	l := d.layout(w, h)
	if w < 64 || h < 18 {
		return ""
	}
	// Wheel input always navigates the active pane, independent of pointer position.
	if e.button == 64 || e.button == 65 {
		d.clickPath = ""
		m := d.panes[d.active]
		if m.loading || m.editing {
			return ""
		}
		delta := 1
		if e.button == 64 {
			delta = -1
		}
		m.cursor = max(0, min(m.cursor+delta, max(0, len(m.visible())-1)))
		m.view(l.rows)
		return ""
	}
	if e.y == 2 && e.button == 0 {
		d.clickPath = ""
		switch {
		case e.x >= 3 && e.x <= 13:
			return "machine"
		case e.x >= 17 && e.x <= 26:
			return "copy"
		case e.x >= 30 && e.x <= 43:
			return "newfolder"
		case e.x >= 47 && e.x <= 57:
			return "refresh"
		case e.x >= 61 && e.x <= 72:
			return "hidden"
		case e.x >= 76 && e.x <= 86:
			return "settings"
		}
	}
	if (e.button == 0 || e.button == 2) && (e.y < 9 || e.y >= 9+l.rows) {
		d.clickPath = ""
	}
	m := d.panes[d.active]
	if l.selectionRows > 0 && e.y >= l.selectionTop && e.y < l.selectionTop+l.selectionRows {
		start := m.selectionStart(l.selectionRows)
		if e.button == 2 {
			items := m.selected()
			at := start + e.y - l.selectionTop
			if at < len(items) {
				m.toggle(items[at])
			}
		}
		return ""
	}
	side := -1
	if e.y >= 4 && e.y < 12+l.rows && e.x >= l.left && e.x < l.left+l.pane {
		side = 0
	}
	if e.y >= 4 && e.y < 12+l.rows && e.x >= l.right && e.x < l.right+l.pane {
		side = 1
	}
	if m.dragging != 0 && e.button&32 != 0 {
		side = d.active
	}
	if side < 0 {
		return ""
	}
	if side != d.active {
		d.clickPath = ""
		if e.button == 0 || e.button == 2 {
			d.active = side
			for _, p := range d.panes {
				p.dragging = 0
			}
		}
		return ""
	}
	m = d.panes[side]
	x := e.x - []int{l.left, l.right}[side] + 1
	if m.loading || m.editing {
		return ""
	}
	if e.y == 7 && e.button == 0 {
		if f := dualSortAt(x, l.pane); f != "" {
			m.setSort(f)
		}
		return ""
	}
	if e.y == 6 && e.button == 0 {
		if x < l.pane-textWidth(m.sortControl())-1 {
			return "filter"
		}
		return "sort"
	}
	if e.y >= 9 && (e.button == 0 || e.button == 2) {
		d.filtering[side] = false
	}
	_, offset := m.view(l.rows)
	v := m.visible()
	if e.button&32 != 0 && m.dragging != 0 {
		_, size := scrollbar(offset, l.rows, len(v))
		thumb := max(0, min(e.y-9-m.dragGrab, l.rows-size))
		m.offset = thumb * max(0, len(v)-l.rows) / max(1, l.rows-size)
		m.cursor = max(m.offset, min(m.cursor, m.offset+l.rows-1))
		return ""
	}
	if e.y < 9 || e.y >= 9+l.rows || e.button&32 != 0 {
		return ""
	}
	if x == l.pane && e.button == 0 && len(v) > l.rows {
		top, size := scrollbar(offset, l.rows, len(v))
		row := e.y - 9
		m.dragGrab = row - top
		if row < top || row >= top+size {
			m.dragGrab = size / 2
			thumb := max(0, min(row-m.dragGrab, l.rows-size))
			m.offset = thumb * (len(v) - l.rows) / max(1, l.rows-size)
			m.cursor = max(m.offset, min(m.cursor, m.offset+l.rows-1))
		}
		m.dragging = 1
		return ""
	}
	at := offset + e.y - 9
	if at >= len(v) || x <= 1 || x >= l.pane {
		return ""
	}
	if e.button != 0 && e.button != 2 {
		return ""
	}
	m.cursor = at
	if e.button == 0 {
		now := time.Now()
		double := d.clickPath == v[at].Path && d.clickSide == side && now.Sub(d.clickAt) <= 450*time.Millisecond
		d.clickPath = v[at].Path
		d.clickSide = side
		d.clickAt = now
		if double {
			d.clickPath = ""
			if v[at].Dir {
				return "open:" + v[at].Path
			}
		}
		return ""
	}
	d.clickPath = ""
	d.toggleFocused()
	return ""
}
