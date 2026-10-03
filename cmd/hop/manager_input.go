package main

import (
	"fmt"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type paneResult struct {
	side    int
	listing browserListing
	focused string
	created bool
}

func (d *dualManager) Run() (managerAction, error) {
	tty, err := openTTY()
	if err != nil {
		return managerAction{}, err
	}
	defer tty.Close()
	old, err := stty(tty, "-g")
	if err != nil {
		return managerAction{}, err
	}
	if _, err = stty(tty, "raw", "-echo", "min", "0", "time", "1"); err != nil {
		return managerAction{}, err
	}
	leave := enterPickerScreen(tty, true)
	defer func() { leave(); _, _ = stty(tty, old) }()
	keys := make(chan []byte, 8)
	done := make(chan struct{})
	readerDone := make(chan struct{})
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
		fields := strings.Fields(v)
		if len(fields) == 2 {
			if n, _ := strconv.Atoi(fields[0]); n > 0 {
				h = n
			}
			if n, _ := strconv.Atoi(fields[1]); n > 0 {
				w = n
			}
		}
	}
	measure()
	results := make(chan paneResult, 4)
	load := func(side int, target string, create bool) {
		m := d.panes[side]
		if m.loading {
			return
		}
		focus := ""
		v := m.visible()
		if m.cursor >= 0 && m.cursor < len(v) && target == m.current {
			focus = v[m.cursor].Path
		}
		m.loading = true
		m.notice = ""
		loader, mkdir := d.loaders[side], d.mkdir[side]
		go func() {
			r := paneResult{side: side, focused: focus, listing: browserListing{Target: path.Clean(target)}}
			if create {
				r.listing.Err = mkdir(target)
				r.created = r.listing.Err == nil
			}
			if r.listing.Err == nil {
				r.listing.Items, r.listing.Err = loader(target)
			}
			select {
			case results <- r:
			case <-done:
			}
		}()
	}
	for i, m := range d.panes {
		m.loading = false
		target := m.current
		if d.next[i] != "" {
			target = d.next[i]
			d.next[i] = ""
		}
		load(i, target, false)
	}
	draw := func() { fmt.Fprint(tty, d.render(w, h)) }
	draw()
	transferResults := make(chan transferResult, 1)
	defer func() {
		if d.transfer != nil && d.transfer.cancel != nil {
			d.transfer.cancel()
		}
		if d.transfer.busy() {
			if d.sftp != nil {
				d.sftp.Abort()
			}
			<-transferResults
		}
	}()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	var pending []byte
	for {
		select {
		case <-tick.C:
			if d.prefix != "" && time.Since(d.prefixAt) > time.Second {
				d.prefix = ""
				draw()
			}
			if d.transfer.busy() {
				draw()
			}
		case r := <-transferResults:
			if d.copyResult(r, transferResults) {
				for i, p := range d.panes {
					load(i, p.current, false)
				}
			}
			draw()
		case sig := <-sigs:
			if sig != syscall.SIGWINCH {
				return managerAction{kind: "quit"}, nil
			}
			measure()
			draw()
		case r := <-results:
			m := d.panes[r.side]
			before := m.current
			m.apply(r.listing)
			if r.listing.Err == nil {
				m.recent = append([]FileItem{{Name: before, Path: before, Dir: true}}, m.recent...)
				if len(m.recent) > 50 {
					m.recent = m.recent[:50]
				}
				for i, f := range m.visible() {
					if f.Path == r.focused {
						m.cursor = i
						break
					}
				}
				if r.created {
					d.notice = "Folder created: " + r.listing.Target
				}
			} else if len(m.items) == 0 && m.current != d.homes[r.side] {
				d.notice = m.notice
				load(r.side, d.homes[r.side], false)
			}
			draw()
		case data := <-keys:
			pending = append(pending, data...)
			for len(pending) > 0 {
				key, n := managerKey(pending)
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
				if _, mouse := parseMouse(key); !mouse {
					d.clickPath = ""
				}
				if d.settings {
					if action, done := d.settingsKey(key, w, h); done {
						return action, nil
					}
					draw()
					continue
				}
				if d.help {
					switch key {
					case "esc", "text:h", "text:?":
						d.help = false
					case "down", "text:j":
						d.helpOffset++
					case "up", "text:k":
						d.helpOffset--
					case "pagedown":
						d.helpOffset += max(1, h-7)
					case "pageup":
						d.helpOffset -= max(1, h-7)
					case "quit", "text:q":
						return managerAction{kind: "quit"}, nil
					}
					if e, ok := parseMouse(key); ok {
						if e.button == 64 {
							d.helpOffset--
						}
						if e.button == 65 {
							d.helpOffset++
						}
					}
					draw()
					continue
				}
				if e, ok := parseMouse(key); ok {
					d.prefix = ""
					if d.transfer != nil && (d.transfer.modal() || (d.transfer.stage == "error" && e.y == h-3)) {
						key = ""
						if !d.transfer.busy() && e.button == 0 && !e.release && e.y == h-3 {
							if index := d.transfer.buttonAt(e.x); index >= 0 {
								d.transfer.button = index
								key = "enter"
							}

						}
					} else {
						key = d.mouse(e, w, h)
					}
					if strings.HasPrefix(key, "open:") {
						load(d.active, key[5:], false)
						draw()
						continue
					}
				}
				if (d.transfer.modal() || (!d.filtering[d.active] && !d.panes[d.active].editing)) && d.transferInput(key, transferResults) {
					draw()
					continue
				}
				if key == "quit" {
					return managerAction{kind: "quit"}, nil
				}
				if d.filtering[d.active] && !d.panes[d.active].editing && (d.transfer == nil || !d.transfer.modal()) {
					if d.filterKey(key) {
						draw()
						continue
					}
				}
				if key == "esc" && !d.panes[d.active].editing && (d.transfer == nil || !d.transfer.modal()) {
					m := d.panes[d.active]
					if len(m.marks) > 0 {
						clear(m.marks)
						m.order = nil
						m.selectionOffset = -1
						d.transfer = nil
						d.prefix = ""
						d.notice = "Selection cleared"
						draw()
						continue
					}
				}
				if d.transfer != nil {
					if d.transfer.busy() {
						continue
					}
					if d.transfer.stage == "confirm" {
						if key == "enter" || (key == "copy" && d.transfer.action.kind == "copy") {
							d.executeCopy(transferResults)
						}
						if key == "esc" {
							d.transfer.cancel()
							d.notice = d.transfer.verb() + " cancelled"
							d.transfer = nil
						}
						draw()
						continue
					}
					if key == "esc" {
						d.transfer = nil
						draw()
						continue
					}
				}
				if w < 64 || h < 18 {
					if key == "esc" {
						return managerAction{kind: "quit"}, nil
					}
					continue
				}
				if key == "panel" {
					d.prefix = ""
					d.active = 1 - d.active
					draw()
					continue
				}
				m := d.panes[d.active]
				if m.editing {
					switch key {
					case "esc":
						m.editing = false
						m.creating = false
						m.notice = ""
					case "clear":
						m.edit = ""
					case "backspace", "hidden":
						r := []rune(m.edit)
						if len(r) > 0 {
							m.edit = string(r[:len(r)-1])
						}
					case "space":
						m.edit += " "
					case "enter":
						target := managerPath(m.current, d.homes[d.active], m.edit)
						if m.creating {
							var e error
							target, e = newFolderPath(m.current, m.edit)
							if e != nil {
								m.notice = e.Error()
								break
							}
						}
						m.editing = false
						create := m.creating
						m.creating = false
						load(d.active, target, create)
					default:
						if strings.HasPrefix(key, "text:") {
							m.edit += key[5:]
						}
					}
					draw()
					continue
				}
				key = d.normalKey(key)
				switch key {
				case "quit":
					return managerAction{kind: "quit"}, nil
				case "settings":
					d.filtering[d.active] = false
					d.openSettings()
				case "filter":
					d.filtering[d.active] = true
				case "help":
					d.help = true
					d.helpOffset = 0
				case "machine":
					d.filtering[d.active] = false
					return managerAction{kind: "machine"}, nil
				case "recent":
					if !d.demo && d.active == 1 && d.sftp == nil {
						d.notice = "Not connected · o Options to reconnect"
						break
					}
					d.filtering[d.active] = false
					if !m.loading {
						return managerAction{kind: "recent", side: d.active}, nil
					}
				case "hidden":
					d.toggleHidden()
				case "copy", "move", "delete":
					if !d.demo && d.sftp == nil && (key != "delete" || d.active == 1) {
						d.notice = "Not connected · o Options to reconnect"
						break
					}
					if d.panes[0].loading || d.panes[1].loading {
						d.notice = "Wait for both folders to finish loading"
						break
					}
					a := d.selectedAction()
					a.kind = key
					if len(a.files) > 0 {
						if d.demo {
							d.notice = "Demo mode · no files copied"
							break
						}
						d.filtering[d.active] = false
						d.beginCopy(a, transferResults)
						break
					}
					d.notice = "Mark a file or folder with Space first"
				case "refresh":
					for i, p := range d.panes {
						load(i, p.current, false)
					}
				case "esc":
					if m.query != "" {
						m.query = ""
						m.cursor = 0
					} else if m.notice != "" {
						m.notice = ""
					} else if d.notice != "" {
						d.notice = ""
					}
				default:
					if m.loading {
						draw()
						continue
					}
					v := m.visible()
					m.cursor = max(0, min(m.cursor, max(0, len(v)-1)))
					l := d.layout(w, h)
					switch key {
					case "up":
						m.cursor = max(0, m.cursor-1)
					case "down":
						m.cursor = min(max(0, len(v)-1), m.cursor+1)
					case "pageup":
						m.cursor = max(0, m.cursor-l.rows)
					case "pagedown":
						m.cursor = min(max(0, len(v)-1), m.cursor+l.rows)
					case "home":
						m.cursor = 0
					case "end":
						m.cursor = max(0, len(v)-1)
					case "space":
						d.toggleFocused()
					case "enter", "right":
						if len(v) > 0 {
							if v[m.cursor].Dir {
								load(d.active, v[m.cursor].Path, false)
							} else if key == "enter" {
								d.toggleFocused()
							}
						}
					case "backspace", "left":
						load(d.active, path.Dir(m.current), false)
					case "path":
						d.filtering[d.active] = false
						m.editing = true
						m.creating = false
						m.edit = m.current
					case "newfolder":
						d.filtering[d.active] = false
						if d.demo || d.readonly {
							d.notice = "Folder creation is disabled in demo mode"
							if !d.demo {
								d.notice = "Preview mode: press o for Options to enable folder creation"
							}
							break
						}
						m.editing = true
						m.creating = true
						m.edit = ""
					case "sort":
						fields := []string{"name", "date", "size", "type"}
						for i, f := range fields {
							if m.sortOrder.Field == f {
								m.setSort(fields[(i+1)%len(fields)])
								break
							}
						}
					case "clear":
						m.query = ""
						m.cursor = 0
					}
				}
				draw()
			}
		}
	}
}
