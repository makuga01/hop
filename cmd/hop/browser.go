package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type FileItem struct {
	Name, Path         string
	Dir, Regular, Link bool
	Size               uint64
	Modified           int64
}
type FileLoader func(string) ([]FileItem, error)
type Browser struct {
	Title, Current, Home string
	Host                 string
	FolderOnly           bool
	Load                 FileLoader
	Mkdir                func(string) error
	BeforeLoad           func()
	Recent               []FileItem
	Updates              <-chan []FileItem
	StartRecent          bool
	Preselected          []FileItem
	Sort                 SortOrder
}
type BrowserResult struct {
	Files     []FileItem
	Directory string
}
type browserListing struct {
	Target string
	Items  []FileItem
	Err    error
}

// Listings are replaced, never edited in place. Cache filtering/sorting until
// the listing or view options change; cursor movement and redraw need no sort.
type listingCache struct {
	source, visible []FileItem
	query           string
	hidden, virtual bool
	order           SortOrder
	valid           bool
}
type browserModel struct {
	listing                               listingCache
	hideDotfiles                          bool
	current                               string
	items, recent                         []FileItem
	marks                                 map[string]FileItem
	order                                 []string
	query, notice, edit                   string
	loadLabel                             string
	cursor, reviewCursor                  int
	offset, reviewOffset, selectionOffset int
	dragging, dragGrab                    int
	loading, virtual, review, editing     bool
	creating                              bool
	sortOrder                             SortOrder
	sorting                               bool
	sortCursor                            int
}

func newBrowserModel(b Browser) *browserModel {
	m := &browserModel{current: b.Current, selectionOffset: -1, marks: map[string]FileItem{}, recent: b.Recent, virtual: b.StartRecent}
	m.sortOrder = b.Sort
	if m.sortOrder.Field == "" {
		m.sortOrder, _ = parseSort("", "")
	}
	if b.StartRecent {
		m.items = b.Recent
	}
	for _, f := range b.Preselected {
		m.marks[f.Path] = f
		m.order = append(m.order, f.Path)
	}
	return m
}
func (m *browserModel) visible() []FileItem {
	if m.review {
		return m.selected()
	}
	c := &m.listing
	same := len(c.source) == len(m.items) && (len(m.items) == 0 || &c.source[0] == &m.items[0])
	if c.valid && same && c.query == m.query && c.hidden == m.hideDotfiles && c.virtual == m.virtual && c.order == m.sortOrder {
		return c.visible
	}
	out := make([]FileItem, 0, len(m.items))
	for _, f := range m.items {
		if m.hideDotfiles && f.Name != ".." && strings.HasPrefix(path.Base(f.Name), ".") {
			continue
		}
		if fuzzy(f.Name, m.query) {
			out = append(out, f)
		}
	}
	if !m.virtual || m.sortOrder.Explicit {
		sortFiles(out, m.sortOrder)
	}
	*c = listingCache{source: m.items, visible: out, query: m.query, hidden: m.hideDotfiles, virtual: m.virtual, order: m.sortOrder, valid: true}
	return out
}
func (m *browserModel) selected() []FileItem {
	out := []FileItem{}
	for _, p := range m.order {
		if f, ok := m.marks[p]; ok {
			out = append(out, f)
		}
	}
	return out
}
func (m *browserModel) toggle(f FileItem) {
	if !markable(f) {
		return
	}
	m.selectionOffset = -1
	if _, ok := m.marks[f.Path]; ok {
		delete(m.marks, f.Path)
		for i, p := range m.order {
			if p == f.Path {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	} else {
		m.marks[f.Path] = f
		m.order = append(m.order, f.Path)
	}
}
func markable(f FileItem) bool {
	return f.Name != ".." && f.Name != "." && (f.Regular || f.Dir || f.Link)
}
func browserError(p string, e error) string {
	var se *StatusError
	if errors.As(e, &se) && se.Code == 3 || os.IsPermission(e) {
		return "Permission denied: " + p + " — choose another folder"
	}
	if noSuch(e) || os.IsNotExist(e) {
		return "Folder not found: " + p
	}
	return "Cannot open " + p + ": " + e.Error()
}
func (m *browserModel) apply(r browserListing) {
	m.loading = false
	if r.Err != nil && len(r.Items) == 0 {
		m.notice = browserError(r.Target, r.Err)
		return
	}
	previous := m.current
	m.offset = 0
	m.current = r.Target
	m.items = r.Items
	m.listing.valid = false
	// Refresh only reconciles marks in the directory actually loaded. Marks in
	// other folders remain selected, and a failed listing must never erase them.
	if r.Err == nil {
		present := make(map[string]FileItem, len(r.Items))
		for _, f := range r.Items {
			present[f.Path] = f
		}
		kept := m.order[:0]
		for _, name := range m.order {
			if path.Dir(name) == r.Target {
				if f, ok := present[name]; ok {
					m.marks[name] = f
				} else {
					delete(m.marks, name)
				}
			}
			if _, ok := m.marks[name]; ok {
				kept = append(kept, name)
			}
		}
		m.order = kept
	}
	m.virtual = false
	m.query = ""
	m.cursor = 0
	m.notice = ""
	if path.Dir(previous) == r.Target {
		for i, f := range m.visible() {
			if f.Path == previous {
				m.cursor = i
				break
			}
		}
	}
	if r.Err != nil {
		m.notice = browserError(r.Target, r.Err)
	}
}
func (m *browserModel) beginReview() bool {
	if len(m.marks) == 0 {
		v := m.visible()
		if m.cursor < len(v) && m.cursor >= 0 && markable(v[m.cursor]) {
			m.toggle(v[m.cursor])
		}
	}
	if len(m.marks) == 0 {
		m.notice = "Mark files or folders with Space first"
		return false
	}
	m.review = true
	m.reviewCursor = 0
	m.reviewOffset = 0
	m.notice = ""
	return true
}
func localFileLoader(p string) ([]FileItem, error) {
	entries, e := os.ReadDir(p)
	if e != nil {
		return nil, e
	}
	out := []FileItem{}
	for _, entry := range entries {
		full := filepath.Join(p, entry.Name())
		linkInfo, e := os.Lstat(full)
		if e != nil {
			continue
		}
		link := linkInfo.Mode()&os.ModeSymlink != 0
		info, e := os.Stat(full)
		if e != nil && link {
			info = linkInfo
			e = nil
		}
		if e != nil {
			continue
		}
		out = append(out, FileItem{Link: link, Name: entry.Name(), Path: full, Dir: info.IsDir(), Regular: info.Mode().IsRegular(), Size: uint64(info.Size()), Modified: info.ModTime().Unix()})
	}
	return sortedListing(p, out), nil
}
func remoteFileLoader(s *SFTP) FileLoader {
	return func(p string) ([]FileItem, error) {
		entries, e := s.ReadDir(p)
		if e != nil && len(entries) == 0 {
			return nil, e
		}
		out := []FileItem{}
		for _, entry := range entries {
			a := entry.Attr
			if a.Mode&0170000 == 0120000 {
				if target, err := s.Stat(path.Join(p, entry.Name), true); err == nil {
					a = target
				}
			}
			out = append(out, FileItem{Link: entry.Attr.Mode&0170000 == 0120000, Name: entry.Name, Path: path.Join(p, entry.Name), Dir: a.Dir(), Regular: a.Regular(), Size: a.Size, Modified: int64(a.Mtime)})
		}
		return sortedListing(p, out), e
	}
}
func sortedListing(p string, out []FileItem) []FileItem {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if path.Clean(p) != "/" {
		out = append([]FileItem{{Name: "..", Path: path.Dir(p), Dir: true}}, out...)
	}
	return out
}
func recentFiles(cs []Candidate) []FileItem {
	out := []FileItem{}
	for _, c := range rank(cs, "") {
		if c.Dir {
			out = append(out, FileItem{Name: c.Path, Path: c.Path, Dir: true, Modified: c.Modified})
		}
	}
	return out
}

// Keep the selection visible without letting it crowd out navigation.
func browserLayout(m *browserModel, h int) (rows, selectionRows int) {
	rows = max(1, h-13)
	if len(m.marks) == 0 || m.review || h < 16 {
		return
	}
	selectionRows = min(len(m.marks), 4, max(1, (h-14)/2))
	height := selectionRows + 3
	if len(m.marks) > selectionRows {
		height++
	}
	rows = max(1, rows-height)
	return
}

func (b Browser) renderSelection(m *browserModel, w, rows int) string {
	selected := m.selected()
	var out strings.Builder
	label := "items"
	if len(selected) == 1 {
		label = "item"
	}
	out.WriteString(uiRule(fmt.Sprintf("Selected · %d %s", len(selected), label), w, true))
	start := m.selectionStart(rows)
	if len(selected) > rows {
		out.WriteString(uiBoxLine(selectionRange(start, rows, len(selected)), w, uiMuted))
	}

	for row, f := range selected[start:min(len(selected), start+rows)] {
		name := f.Path
		if rel, err := filepath.Rel(b.Current, f.Path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			name = rel
		}
		size := humanSize(f.Size)
		if f.Dir {
			name += "/"
			size = "<DIR>"
		}
		width := w - 6
		if w >= 50 {
			width -= 12
		}
		line := " * " + fitEnd(name, width)
		if w >= 50 {
			line += "  " + fitRight(size, 10)
		}
		out.WriteString(scrollLine(line, w, uiMarked, row, start, rows, len(selected)))
	}
	out.WriteString(uiBottom(w))
	return out.String()
}

func (b Browser) render(m *browserModel, w, h int) string {
	if m.sorting {
		return renderSortMenu(m, w, h)
	}
	screenWidth := w
	_, w = contentGeometry(w)
	rows, selectionRows := browserLayout(m, h)
	var out strings.Builder
	title := b.Title
	if m.review {
		title = "Review selected files and folders"
	}
	location := m.current
	if m.virtual {
		location = "Recent locations"
	}
	if m.review {
		location = "Selection across all visited folders"
	}
	label := "LOCAL"
	if b.Host != "" {
		label = "REMOTE"
	}
	if m.virtual {
		label = "RECENT"
	}
	if m.review {
		label = "REVIEW"
	}
	out.WriteString(uiSpans(w, uiBase, uiSpan{label + "  ", uiMuted}, uiSpan{fitEnd(location, w-textWidth(label)-2), uiBase}) + "\r\n")
	out.WriteString(uiRule("", w, true))
	filter := " Filter: " + m.query
	if m.editing {
		filter = " Go to: " + m.edit + "_"
		if m.creating {
			filter = " New folder: " + m.edit + "_"
		}
	}
	if m.review {
		filter = " Enter continues · Folders include their contents"
	}
	if !m.editing && !m.review {
		control := m.sortControl()
		filter = fit(filter, max(0, w-4-textWidth(control))) + "  " + control
	}
	out.WriteString(uiBoxLine(filter, w, uiHeader))
	nameWidth, withSize, withDate, withType := browserColumns(w)
	columns := "  " + fit(m.sortLabel("name", "Name"), nameWidth)
	if withSize {
		columns += "  " + fit(m.sortLabel("size", "Size"), 10)
	}
	if withDate {
		columns += "  " + fit(m.sortLabel("date", "Modified"), 16)
	}
	if withType {
		columns += "  " + fit(m.sortLabel("type", "Type"), 10)
	}
	out.WriteString(uiBoxLine(columns, w, uiMuted))
	out.WriteString(uiRule("", w, false))
	visible := m.visible()
	cursor, start := m.view(rows)
	for row := 0; row < rows; row++ {
		i := start + row
		line := ""
		dateAt, dateEnd := 0, 0
		style := uiBase
		if i < len(visible) {
			f := visible[i]
			if f.Dir {
				style = uiFolder
			}
			marked := false
			if _, marked = m.marks[f.Path]; marked {
				style = uiMarked
			}
			name := f.Name
			if m.review {
				name = f.Path
			}
			if f.Dir && name != ".." {
				name += "/"
			}
			mark := " "
			if marked {
				mark = "*"
			}
			glyph := "· "
			if f.Dir {
				glyph = "▸ "
			}
			if f.Name == ".." {
				glyph = "↑ "
			}
			line = " " + mark + " " + glyph + fit(name, nameWidth-3)
			if withSize {
				size := humanSize(f.Size)
				if f.Dir {
					size = "<DIR>"
				} else if !f.Regular {
					size = "<LINK>"
				}
				line += "  " + fitRight(size, 10)
			}
			if withDate {
				date := ""
				if f.Modified > 0 {
					date = time.Unix(f.Modified, 0).Format("02 Jan 15:04")
				}
				dateAt = len(line)
				line += "  " + fit(date, 16)
				dateEnd = len(line)
			}
			if withType {
				line += "  " + fit(fileType(f), 10)
			}
			if i == cursor {
				style = uiSelected
			}
		} else if row == 0 {
			line = "  Empty folder"
			if m.query != "" {
				line = "  No matching files"
			}
			if m.loading {
				line = "  Loading…"
			}
		}
		var spans []uiSpan
		if dateAt > 0 && i != cursor {
			spans = []uiSpan{{line[:dateAt], style}, {line[dateAt:dateEnd], uiMuted}, {line[dateEnd:], style}}
		}
		out.WriteString(scrollLine(line, w, style, row, start, rows, len(visible), spans...))
	}
	out.WriteString(uiRule("", w, false))
	var total uint64
	dirs := 0
	for _, f := range m.marks {
		if f.Dir {
			dirs++
		} else {
			total += f.Size
		}
	}
	summary := fmt.Sprintf(" %d marked · %s", len(m.marks), humanSize(total))
	if dirs > 0 {
		summary = fmt.Sprintf(" %d marked · %d folders (recursive)", len(m.marks), dirs)
	}
	if b.FolderOnly {
		summary = " Choose a destination folder"
	}
	summary += fmt.Sprintf("    %d entries", len(visible))
	if len(m.marks) > 0 && !m.review && selectionRows == 0 {
		selected := m.selected()
		latest := selected[len(selected)-1]
		name := latest.Path
		if latest.Dir {
			name += "/"
		}
		prefix := fmt.Sprintf(" Selected (%d): ", len(selected))
		summary = prefix + fitEnd(name, w-2-textWidth(prefix))
	}
	out.WriteString(uiBoxLine(summary, w, uiMuted))
	out.WriteString(uiBottom(w))
	if selectionRows > 0 {
		out.WriteString(uiLine("", w, uiBase))
		out.WriteString(b.renderSelection(m, w, selectionRows))
	}
	status := " ↑↓ Move · Space Mark · Enter Open · Right-click Mark"
	style := uiMuted
	if m.notice != "" {
		status = m.notice
		style = uiError
	}
	if m.loading {
		status = "Opening folder…"
		if m.loadLabel != "" {
			status = m.loadLabel
		}
		style = uiMuted
	}
	out.WriteString(uiLine(" "+status, w, style))
	keys := " Tab Review   Ctrl-O Sort   Ctrl-R Recent   Ctrl-L Path"
	if b.FolderOnly {
		keys = " Tab Use folder   Ctrl-N New folder   Ctrl-R Recent   Ctrl-L Path"
	}
	if m.review {
		keys = " Enter Continue   Space Remove   Esc Back to files"
	}
	if m.editing && m.creating {
		keys = " Enter Create folder   Esc Cancel"
	}
	return uiScreen(out.String(), title, b.Host, keys, screenWidth, h)
}

func (b Browser) Run() (BrowserResult, error) {
	tty, e := openTTY()
	if e != nil {
		return BrowserResult{}, errors.New("file selection requires a terminal")
	}
	defer tty.Close()
	old, e := stty(tty, "-g")
	if e != nil {
		return BrowserResult{}, e
	}
	if _, e = stty(tty, "raw", "-echo", "min", "0", "time", "1"); e != nil {
		return BrowserResult{}, e
	}
	leaveScreen := enterPickerScreen(tty, true)
	defer func() { leaveScreen(); _, _ = stty(tty, old) }()
	keys := make(chan []byte, 8)
	done, readerDone := make(chan struct{}), make(chan struct{})
	defer func() { close(done); <-readerDone }()
	go func() {
		defer close(readerDone)
		buf := make([]byte, 128)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, _ := syscall.Read(int(tty.Fd()), buf)
			if n > 0 {
				data := append([]byte{}, buf[:n]...)
				select {
				case keys <- data:
				case <-done:
					return
				}
			}
		}
	}()
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGWINCH)
	defer signal.Stop(sigs)
	w, h := 80, 24
	measure := func() {
		v, _ := stty(tty, "size")
		parts := strings.Fields(v)
		if len(parts) == 2 {
			if n, _ := strconv.Atoi(parts[0]); n > 0 {
				h = n
			}
			if n, _ := strconv.Atoi(parts[1]); n > 0 {
				w = n
			}
		}
	}
	measure()
	m := newBrowserModel(b)
	loads := make(chan browserListing, 1)
	type folderResult struct {
		target  string
		items   []FileItem
		err     error
		created bool
	}
	createdFolders := make(chan folderResult, 1)
	createFolder := func() {
		target, err := newFolderPath(m.current, m.edit)
		if err != nil {
			m.notice = err.Error()
			return
		}
		m.editing = false
		m.loading = true
		m.loadLabel = "Creating folder…"
		m.notice = ""
		if b.BeforeLoad != nil {
			b.BeforeLoad()
		}
		go func() {
			r := folderResult{target: target}
			r.err = b.Mkdir(target)
			if r.err == nil {
				r.created = true
				r.items, r.err = b.Load(target)
			}
			select {
			case createdFolders <- r:
			case <-done:
			}
		}()
	}
	load := func(target string) {
		if m.loading {
			return
		}
		if b.BeforeLoad != nil {
			b.BeforeLoad()
		}
		target = path.Clean(target)
		m.loading = true
		m.loadLabel = "Opening folder…"
		m.notice = ""
		go func() {
			items, err := b.Load(target)
			select {
			case loads <- browserListing{target, items, err}:
			case <-done:
			}
		}()
	}
	if !b.StartRecent {
		load(b.Current)
	}
	draw := func() { fmt.Fprint(tty, b.render(m, w, h)) }
	draw()
	var pending []byte
	for {
		select {
		case sig := <-sigs:
			if sig != syscall.SIGWINCH {
				return BrowserResult{}, errCancelled
			}
			measure()
			draw()
		case result := <-createdFolders:
			m.loading = false
			m.loadLabel = ""
			m.creating = false
			if result.err != nil {
				if result.created {
					m.notice = "Folder created, but cannot open it: " + result.err.Error()
				} else {
					m.notice = "Cannot create folder: " + result.err.Error()
					m.creating = true
					m.editing = true
				}
			} else {
				m.apply(browserListing{Target: result.target, Items: result.items})
				m.notice = "Folder created · Tab to use it"
			}
			draw()
		case result := <-loads:
			m.apply(result)
			draw()
		case update, ok := <-b.Updates:
			if !ok {
				b.Updates = nil
				continue
			}
			m.recent = update
			if m.virtual && !m.review {
				selected := ""
				v := m.visible()
				if m.cursor < len(v) {
					selected = v[m.cursor].Path
				}
				m.items = update
				m.cursor = 0
				for i, f := range m.visible() {
					if f.Path == selected {
						m.cursor = i
						break
					}
				}
			}
			draw()
		case data := <-keys:
			pending = append(pending, data...)
			for len(pending) > 0 {
				key, n := browserKey(pending)
				if n == 0 {
					select {
					case more := <-keys:
						pending = append(pending, more...)
						continue
					case <-time.After(60 * time.Millisecond):
						key = "esc"
						n = len(pending)
					}
				}
				pending = pending[n:]
				if strings.HasPrefix(key, "mouse:") {
					if event, ok := parseMouse(key); ok {
						if target := b.mouse(m, event, w, h); target != "" {
							load(target)
						}
					}
					draw()
					continue
				}
				if key == "quit" {
					return BrowserResult{}, errCancelled
				}
				if m.sorting {
					switch key {
					case "esc":
						m.sorting = false
					case "up":
						m.sortCursor = max(0, m.sortCursor-1)
					case "down":
						m.sortCursor = min(5, m.sortCursor+1)
					case "enter":
						m.chooseSort(m.sortCursor)
					}
					draw()
					continue
				}
				if m.editing {
					switch key {
					case "esc":
						m.editing = false
						m.creating = false
						m.notice = ""
					case "enter":
						if m.creating {
							createFolder()
							break
						}
						p := m.edit
						m.editing = false
						if p != "" {
							p = expandRemote(p, b.Home)
							if !strings.HasPrefix(p, "/") {
								p = path.Join(m.current, p)
							}
							load(p)
						}
					case "backspace":
						r := []rune(m.edit)
						if len(r) > 0 {
							m.edit = string(r[:len(r)-1])
						}
					case "clear":
						m.edit = ""
					default:
						if strings.HasPrefix(key, "text:") {
							m.edit += key[5:]
						} else if key == "space" {
							m.edit += " "
						}
					}
					draw()
					continue
				}
				if m.review {
					v := m.selected()
					switch key {
					case "esc":
						m.review = false
					case "up":
						m.reviewCursor = max(0, m.reviewCursor-1)
					case "down":
						m.reviewCursor = min(max(0, len(v)-1), m.reviewCursor+1)
					case "space":
						if len(v) > 0 {
							m.toggle(v[min(m.reviewCursor, len(v)-1)])
							m.reviewCursor = min(m.reviewCursor, max(0, len(m.marks)-1))
						}
					case "enter", "accept":
						if len(v) > 0 {
							return BrowserResult{Files: v}, nil
						}
					}
					draw()
					continue
				}
				v := m.visible()
				m.cursor = max(0, min(m.cursor, max(0, len(v)-1)))
				f := FileItem{}
				if len(v) > 0 {
					f = v[m.cursor]
				}
				switch key {
				case "up":
					m.cursor = max(0, m.cursor-1)
				case "down":
					m.cursor = min(max(0, len(v)-1), m.cursor+1)
				case "pageup":
					rows, _ := browserLayout(m, h)
					m.cursor = max(0, m.cursor-rows)
				case "pagedown":
					rows, _ := browserLayout(m, h)
					m.cursor = min(max(0, len(v)-1), m.cursor+rows)
				case "home":
					m.cursor = 0
				case "end":
					m.cursor = max(0, len(v)-1)
				case "space":
					if b.FolderOnly {
						m.notice = "Enter opens a folder; Tab uses the current folder"
					} else if markable(f) {
						m.toggle(f)
						m.notice = ""
					} else {
						m.notice = "Mark a file or folder; the parent entry cannot be marked"
					}
				case "enter", "right":
					if m.loading {
						break
					}
					if f.Dir {
						load(f.Path)
					} else if key == "enter" && !b.FolderOnly {
						m.beginReview()
					}
				case "accept":
					if m.loading {
						break
					}
					if b.FolderOnly {
						if m.virtual {
							m.notice = "Open a folder first, then press Tab"
						} else {
							return BrowserResult{Directory: m.current}, nil
						}
					} else {
						m.beginReview()
					}
				case "left", "backspace":
					if m.query != "" && key == "backspace" {
						r := []rune(m.query)
						m.query = string(r[:len(r)-1])
						m.cursor = 0
					} else if !m.loading {
						load(path.Dir(m.current))
					}
				case "recent":
					if !m.loading {
						m.virtual = true
						m.items = m.recent
						m.cursor = 0
						m.query = ""
						m.notice = ""
					}
				case "sort":
					m.openSort()
				case "newfolder":
					if m.loading {
						break
					}
					if !b.FolderOnly || b.Mkdir == nil {
						m.notice = "New folders can be created in the destination picker"
						break
					}
					if m.virtual {
						m.notice = "Open a destination folder first, then press Ctrl-N"
						break
					}
					m.editing = true
					m.creating = true
					m.edit = ""
					m.notice = ""
				case "path":
					if m.loading {
						break
					}
					m.creating = false
					m.editing = true
					m.edit = m.current
				case "clear":
					m.query = ""
					m.cursor = 0
				case "esc":
					if m.query != "" {
						m.query = ""
						m.cursor = 0
					} else if m.notice != "" {
						m.notice = ""
					} else {
						return BrowserResult{}, errCancelled
					}
				default:
					if strings.HasPrefix(key, "text:") {
						text := key[5:]
						if text != "/" || m.query != "" {
							m.query += text
						}
						m.cursor = 0
						m.notice = ""
					}
				}
				draw()
			}
		}
	}
}
func browserKey(p []byte) (string, int) {
	if len(p) == 0 {
		return "", 0
	}
	if p[0] == 27 {
		if len(p) == 1 {
			return "", 0
		}
		if p[1] != '[' && p[1] != 'O' {
			return "esc", 1
		}
		// A terminal may keep legacy mouse encoding despite the SGR request.
		// Its three bytes are one packet, never keyboard commands.
		if len(p) >= 3 && string(p[:3]) == "\x1b[M" {
			if len(p) < 6 {
				return "", 0
			}
			if p[3] < 32 || p[4] <= 32 || p[5] <= 32 {
				return "", 6
			}
			button := int(p[3]) - 32
			end := "M"
			if button&3 == 3 && button&64 == 0 {
				end = "m"
			}
			return fmt.Sprintf("mouse:%d;%d;%d%s", button, int(p[4])-32, int(p[5])-32, end), 6
		}
		for i := 2; i < len(p); i++ {
			if p[i] >= 0x40 && p[i] <= 0x7e {
				seq := string(p[:i+1])
				if key := mouseKey(seq); key != "" {
					return key, i + 1
				}
				keys := map[string]string{"\x1b[A": "up", "\x1b[B": "down", "\x1b[C": "right", "\x1b[D": "left", "\x1b[H": "home", "\x1b[F": "end", "\x1b[1~": "home", "\x1b[4~": "end", "\x1b[5~": "pageup", "\x1b[6~": "pagedown", "\x1bOQ": "recent", "\x1b[12~": "recent", "\x1b[15~": "accept"}
				return keys[seq], i + 1
			}
		}
		return "", 0
	}
	switch p[0] {
	case 3, 4:
		return "quit", 1
	case 13, 10:
		return "enter", 1
	case 32:
		return "space", 1
	case 127, 8:
		return "backspace", 1
	case 12:
		return "path", 1
	case 21:
		return "clear", 1
	case 9, 20:
		return "accept", 1
	case 18:
		return "recent", 1
	case 14:
		return "newfolder", 1
	case 15:
		return "sort", 1
	}
	if !utf8.FullRune(p) {
		return "", 0
	}
	r, n := utf8.DecodeRune(p)
	if r >= 32 && r != utf8.RuneError {
		return "text:" + string(r), n
	}
	return "", n
}

func newFolderPath(parent, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00\r\n") {
		return "", errors.New("Enter one folder name, without slashes")
	}
	return path.Join(parent, name), nil
}
