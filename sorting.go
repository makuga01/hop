package main

import (
	"cmp"
	"fmt"
	"path"
	"sort"
	"strings"
)

type SortOrder struct {
	Field          string
	Desc, Explicit bool
}

func parseSort(field, order string) (SortOrder, error) {
	explicit := field != "" || order != ""
	if field == "" {
		field = "name"
	}
	if !contains([]string{"name", "date", "size", "type"}, field) {
		return SortOrder{}, fmt.Errorf("invalid --sort %q; use name, date, size, or type", field)
	}
	desc := field == "date" || field == "size"
	if order != "" {
		if order != "asc" && order != "desc" {
			return SortOrder{}, fmt.Errorf("invalid --order %q; use asc or desc", order)
		}
		desc = order == "desc"
	}
	return SortOrder{field, desc, explicit}, nil
}
func fileType(f FileItem) string {
	if f.Dir {
		return "Folder"
	}
	if !f.Regular {
		return "Link"
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(f.Name)), ".")
	if ext == "" {
		return "File"
	}
	return ext
}
func sortFiles(items []FileItem, order SortOrder) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if (a.Name == "..") != (b.Name == "..") {
			return a.Name == ".."
		}
		if a.Dir != b.Dir {
			return a.Dir
		}
		comparison := 0
		switch order.Field {
		case "date":
			comparison = cmp.Compare(a.Modified, b.Modified)
		case "size":
			if !a.Dir {
				comparison = cmp.Compare(a.Size, b.Size)
			}
		case "type":
			comparison = cmp.Compare(fileType(a), fileType(b))
		default:
			comparison = cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if comparison != 0 {
			if order.Desc {
				return comparison > 0
			}
			return comparison < 0
		}
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c < 0
		}
		return a.Path < b.Path
	})
}
func (m *browserModel) setSort(field string) {
	before := m.visible()
	focused := ""
	if m.cursor >= 0 && m.cursor < len(before) {
		focused = before[m.cursor].Path
	}
	if m.sortOrder.Field == field && (!m.virtual || m.sortOrder.Explicit) {
		m.sortOrder.Desc = !m.sortOrder.Desc
	} else {
		m.sortOrder, _ = parseSort(field, "")
	}
	m.sortOrder.Explicit = true
	m.sorting = false
	m.notice = ""
	m.cursor = 0
	for i, f := range m.visible() {
		if f.Path == focused {
			m.cursor = i
			break
		}
	}
}
func (m *browserModel) sortLabel(field, label string) string {
	if !m.review && (!m.virtual || m.sortOrder.Explicit) && m.sortOrder.Field == field {
		if m.sortOrder.Desc {
			return label + " ↓"
		}
		return label + " ↑"
	}
	return label
}
func (m *browserModel) sortControl() string {
	if m.virtual && !m.sortOrder.Explicit {
		return "Sort: Suggested"
	}
	return "Sort: " + m.sortLabel(m.sortOrder.Field, strings.ToUpper(m.sortOrder.Field[:1])+m.sortOrder.Field[1:])
}
func (m *browserModel) openSort() {
	if m.review || m.editing || m.loading {
		return
	}
	m.sorting = true
	m.sortCursor = 0
	for i, f := range []string{"name", "date", "size", "type"} {
		if f == m.sortOrder.Field {
			m.sortCursor = i
		}
	}
}
func (m *browserModel) chooseSort(index int) {
	if index < 4 {
		m.setSort([]string{"name", "date", "size", "type"}[index])
		return
	}
	desired := index == 5
	if !m.sortOrder.Explicit && m.virtual {
		m.setSort(m.sortOrder.Field)
	}
	if desired != m.sortOrder.Desc {
		m.setSort(m.sortOrder.Field)
	}
	m.sorting = false
}
func renderSortMenu(m *browserModel, w, h int) string {
	screenWidth := w
	_, w = contentGeometry(w)
	var out strings.Builder
	out.WriteString(uiLine("SORT  File order", w, uiMuted))
	out.WriteString(uiRule("Sort order", w, true))
	out.WriteString(uiBoxLine(" Choose a field or direction", w, uiMuted))
	out.WriteString(uiRule("", w, false))
	labels := []string{"Name", "Date modified", "Size", "Type / extension", "Ascending", "Descending"}
	for i, label := range labels {
		style := uiBase
		if i == m.sortCursor {
			style = uiSelected
		}
		out.WriteString(uiBoxLine(" "+label, w, style))
	}
	out.WriteString(uiBottom(w))
	return uiScreen(out.String(), "Sort files", "", "↑↓ Move   Enter Choose   Esc Back", screenWidth, h)
}

// Match the same responsive columns used by the renderer.
func browserColumns(w int) (name int, size, date, kind bool) {
	name = w - 6
	size = w >= 50
	date = w >= 70
	kind = w >= 100
	if size {
		name -= 12
	}
	if date {
		name -= 18
	}
	if kind {
		name -= 12
	}
	return
}
func sortHeaderAt(x, w int) string {
	name, size, date, kind := browserColumns(w)
	if x < 2 || x >= w {
		return ""
	}
	if x <= name+3 {
		return "name"
	}
	x -= name + 3
	if size {
		if x <= 12 {
			return "size"
		}
		x -= 12
	}
	if date {
		if x <= 18 {
			return "date"
		}
		x -= 18
	}
	if kind {
		return "type"
	}
	return ""
}
