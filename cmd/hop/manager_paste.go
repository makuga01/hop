package main

import (
	"bytes"
	"strings"
	"unicode"
)

const pasteStart = "\x1b[200~"
const pasteEnd = "\x1b[201~"
const maxPaste = 64 << 10

type terminalPaste struct {
	truncated bool
	active    bool
	text      []byte
}

// Consume pasted input before key decoding: a pasted newline must not confirm
// a dialog, and pasted command letters must not copy, move, or delete files.
func (p *terminalPaste) read(pending *[]byte) (string, bool) {
	if !p.active && bytes.HasPrefix(*pending, []byte(pasteStart)) {
		*pending = (*pending)[len(pasteStart):]
		p.active = true
		p.truncated = false
		p.text = nil
	}
	if !p.active {
		return "", false
	}
	end := bytes.Index(*pending, []byte(pasteEnd))
	n := end
	if n < 0 {
		n = max(0, len(*pending)-len(pasteEnd)+1)
	}
	take := min(n, maxPaste-len(p.text))
	if take < n {
		p.truncated = true
	}
	p.text = append(p.text, (*pending)[:take]...)
	*pending = (*pending)[n:]
	if end < 0 {
		return "", true
	}
	*pending = (*pending)[len(pasteEnd):]
	p.active = false
	if p.truncated {
		p.text = nil
		return "paste-too-large", true
	}
	text := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.Trim(string(p.text), "\r\n"))
	p.text = nil
	return "paste:" + text, true
}
func (d *dualManager) pasteText(text string) {
	switch {
	case d.details != "", d.help:
	case d.settings && d.settingsEdit:
		d.settingsText += text
	case d.settings:
	case d.transfer.modal():
		// In particular, ignore paste in confirmation and conflict dialogs.
	case d.panes[d.active].editing && !d.panes[d.active].loading:
		d.panes[d.active].edit += text
	case d.filtering[d.active]:
		d.panes[d.active].query += text
		d.panes[d.active].cursor = 0
	default:
		d.notice = "Paste into a field: P for path, / for filter"
	}
}
