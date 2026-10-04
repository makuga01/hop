package main

import (
	"fmt"
	"strconv"
	"strings"
)

const mouseOn = "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
const mouseOff = "\x1b[?1002l\x1b[?1000l\x1b[?1006l"

type mouseEvent struct {
	button, x, y int
	release      bool
}

func parseMouse(key string) (mouseEvent, bool) {
	var e mouseEvent
	if !strings.HasPrefix(key, "mouse:") {
		return e, false
	}
	s := key[6:]
	if len(s) < 2 {
		return e, false
	}
	last := s[len(s)-1]
	if last != 'M' && last != 'm' {
		return e, false
	}
	parts := strings.Split(s[:len(s)-1], ";")
	if len(parts) != 3 {
		return e, false
	}
	values := []*int{&e.button, &e.x, &e.y}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 100000 {
			return e, false
		}
		*values[i] = n
	}
	e.button &^= 4 | 8 | 16 // Shift, Meta, Ctrl do not change the underlying action.
	e.release = last == 'm'
	return e, e.x > 0 && e.y > 0
}

func viewport(cursor, offset, rows, count int) (int, int) {
	cursor = max(0, min(cursor, max(0, count-1)))
	offset = max(0, min(offset, max(0, count-rows)))
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+rows {
		offset = cursor - rows + 1
	}
	return cursor, offset
}
func (m *browserModel) view(rows int) (cursor, offset int) {
	if m.review {
		m.reviewCursor, m.reviewOffset = viewport(m.reviewCursor, m.reviewOffset, rows, len(m.selected()))
		return m.reviewCursor, m.reviewOffset
	}
	m.cursor, m.offset = viewport(m.cursor, m.offset, rows, len(m.visible()))
	return m.cursor, m.offset
}
func (m *browserModel) selectionStart(rows int) int {
	end := max(0, len(m.marks)-rows)
	if m.selectionOffset < 0 {
		return end
	}
	m.selectionOffset = max(0, min(m.selectionOffset, end))
	return m.selectionOffset
}
func scrollbar(offset, rows, count int) (top, size int) {
	if count <= rows {
		return 0, rows
	}
	size = max(1, rows*rows/count)
	top = offset * (rows - size) / max(1, count-rows)
	return
}
func scrollLine(line string, w int, style string, row, offset, rows, count int, spans ...uiSpan) string {
	edge := "│"
	if count > rows {
		top, size := scrollbar(offset, rows, count)
		edge = "░"
		if row >= top && row < top+size {
			edge = "█"
		}
	}
	middle := uiPaint(fit(line, w-2), style)
	if len(spans) > 0 {
		middle = uiSpans(w-2, style, spans...)
	}
	return uiPaint("│", uiBorder) + middle + uiPaint(edge, uiBorder) + "\r\n"
}

type mouseRegion struct {
	top, rows, count, offset int
	selection                bool
}

func (b Browser) mouse(m *browserModel, e mouseEvent, w, h int) string {
	if e.release {
		m.dragging = 0
		return ""
	}
	gutter, inner := contentGeometry(w)
	e.x -= gutter
	w = inner + 1
	if m.sorting {
		e.y -= 2
		if e.button == 0 && e.x >= 2 && e.x < max(20, w-1) && e.y >= 5 && e.y <= 10 {
			m.chooseSort(e.y - 5)
		}
		return ""
	}
	if !m.editing && !m.loading && !m.review && e.button == 0 {
		if e.y == 6 {
			if field := sortHeaderAt(e.x, max(20, w-1)); field != "" {
				m.setSort(field)
			}
			return ""
		}
		if e.y == 5 && e.x >= max(20, w-1)-1-textWidth(m.sortControl()) && e.x < max(20, w-1) {
			m.openSort()
			return ""
		}
	}
	if m.editing || m.loading {
		return ""
	}
	rows, selectionRows := browserLayout(m, h)
	_, offset := m.view(rows)
	region := mouseRegion{top: 8, rows: rows, count: len(m.visible()), offset: offset}
	selection := mouseRegion{top: 13 + rows, rows: selectionRows, count: len(m.marks), selection: true}
	if selectionRows > 0 {
		selection.offset = m.selectionStart(selectionRows)
		if selection.count > selectionRows {
			selection.top++
		}
	}
	if m.dragging == 2 || selectionRows > 0 && e.y >= selection.top && e.y < selection.top+selection.rows {
		region = selection
	}
	if m.dragging == 1 {
		region = mouseRegion{top: 8, rows: rows, count: len(m.visible()), offset: offset}
	}
	if region.rows == 0 || region.count == 0 {
		return ""
	}
	setOffset := func(n int) {
		n = max(0, min(n, max(0, region.count-region.rows)))
		if region.selection {
			m.selectionOffset = n
			return
		}
		if m.review {
			m.reviewOffset = n
			m.reviewCursor = max(n, min(m.reviewCursor, min(region.count-1, n+region.rows-1)))
		} else {
			m.offset = n
			m.cursor = max(n, min(m.cursor, min(region.count-1, n+region.rows-1)))
		}
	}
	motion := e.button&32 != 0
	button := e.button & 3
	if motion && m.dragging != 0 {
		_, size := scrollbar(region.offset, region.rows, region.count)
		thumb := max(0, min(e.y-region.top-m.dragGrab, region.rows-size))
		setOffset((thumb*max(0, region.count-region.rows) + max(1, region.rows-size)/2) / max(1, region.rows-size))
		return ""
	}
	if motion {
		return ""
	}
	if e.x < 2 || e.x > max(20, w-1) || e.y < region.top || e.y >= region.top+region.rows {
		return ""
	}
	if e.button&64 != 0 {
		step := 0
		if button == 0 {
			step = -1
		} else if button == 1 {
			step = 1
		}
		if step == 0 {
			return ""
		}
		if region.selection {
			setOffset(region.offset + step)
		} else {
			// Wheel steps have the same cursor behavior as Up/Down. The
			// viewport moves only when the cursor crosses a visible edge.
			if m.review {
				m.reviewCursor += step
			} else {
				m.cursor += step
			}
			m.view(region.rows)
		}
		return ""
	}
	if e.x == max(20, w-1) {
		if button != 0 || region.count <= region.rows {
			return ""
		}
		top, size := scrollbar(region.offset, region.rows, region.count)
		row := e.y - region.top
		if row >= top && row < top+size {
			m.dragGrab = row - top
		} else {
			m.dragGrab = size / 2
			thumb := max(0, min(row-m.dragGrab, region.rows-size))
			setOffset(thumb * (region.count - region.rows) / max(1, region.rows-size))
		}
		m.dragging = 1
		if region.selection {
			m.dragging = 2
		}
		return ""
	}
	if button != 0 && button != 2 {
		return ""
	}
	index := region.offset + e.y - region.top
	if index >= region.count {
		return ""
	}
	if region.selection {
		if b.FolderOnly {
			return ""
		}
		if button == 2 {
			m.toggle(m.selected()[index])
		} else {
			m.beginReview()
			m.reviewCursor = index
		}
		return ""
	}
	if m.review {
		m.reviewCursor = index
		if button == 2 {
			m.toggle(m.selected()[index])
		}
		return ""
	}
	m.cursor = index
	f := m.visible()[index]
	if button == 0 && f.Dir {
		return f.Path
	}
	if !b.FolderOnly && markable(f) {
		m.toggle(f)
		m.notice = ""
	}
	return ""
}

func mouseKey(seq string) string {
	if strings.HasPrefix(seq, "\x1b[<") {
		return "mouse:" + seq[3:]
	}
	return ""
}

func selectionRange(start, rows, count int) string {
	return fmt.Sprintf(" %d–%d of %d · Wheel to scroll", start+1, min(count, start+rows), count)
}
