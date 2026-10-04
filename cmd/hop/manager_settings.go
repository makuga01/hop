package main

import (
	"strconv"
	"strings"
)

func (d *dualManager) openSettings() {
	d.settings = true
	d.settingsEdit = false
	d.settingsError = ""
	d.settingsCursor = 0
	d.settingsOffset = 0
	if d.connection[0] != "" {
		return
	}
	d.connection = [5]string{d.host.Target}
	merged := withOverrides(d.host, d.options.SSH)
	for i := 0; i+1 < len(merged.Options); i++ {
		for j, flag := range []string{"-p", "-i", "-J", "-F"} {
			if merged.Options[i] == flag {
				d.connection[j+1] = merged.Options[i+1]
			}
		}
	}
}
func (d *dualManager) settingsRows() [][2]string {
	toggle := func(b bool) string {
		if b {
			return "On"
		}
		return "Off"
	}
	overwrite := "Ask on conflict"
	if d.options.Overwrite {
		overwrite = "Replace existing files"
	}
	direction := "Ascending"
	if d.panes[d.active].sortOrder.Desc {
		direction = "Descending"
	}
	rows := [][2]string{{"Existing files", overwrite}, {"Preview only", toggle(d.options.DryRun)}, {"Remote history", toggle(!d.options.NoHistory)}, {"Theme", themeLabel()}, {"Active panel sort", d.panes[d.active].sortOrder.Field}, {"Sort direction", direction}}
	for i, label := range []string{"SSH destination", "SSH port", "Identity file", "Jump host", "SSH config file"} {
		value := d.connection[i]
		if value == "" {
			value = "SSH default"
		}
		rows = append(rows, [2]string{label, value})
	}
	return append(rows, [2]string{"Connect / reconnect", "Apply connection fields"})
}
func (d *dualManager) settingsView(w, h int) string {
	_, width := contentGeometry(w)
	rows := d.settingsRows()
	count := max(1, h-8)
	d.settingsCursor = max(0, min(d.settingsCursor, len(rows)-1))
	_, d.settingsOffset = viewport(d.settingsCursor, d.settingsOffset, count, len(rows))
	var body strings.Builder
	body.WriteString(uiRule("Options · this session", width, true))
	for i := d.settingsOffset; i < min(len(rows), d.settingsOffset+count); i++ {
		style := uiBase
		if i == d.settingsCursor {
			style = uiSelected
		}
		value := rows[i][1]
		if d.settingsEdit && i == d.settingsCursor {
			value = d.settingsText + "_"
		}
		body.WriteString(uiBoxLine(" "+fit(rows[i][0], min(25, width/2))+"  "+value, width, style))
	}
	body.WriteString(uiBottom(width))
	status := "Enter / click changes a value · connection fields apply on Connect"
	if d.settingsError != "" {
		status = d.settingsError
	}
	body.WriteString(uiLine(status, width, uiMuted))
	footer := "j/k Choose   Enter Change   Esc Back"
	if d.settingsEdit {
		footer = "Type a value   Ctrl-U Clear   Enter Save   Esc Cancel"
	}
	return uiScreen(body.String(), "Options", d.host.Label(), footer, w, h)
}
func (d *dualManager) settingsKey(key string, w, h int) (managerAction, bool) {
	if key == "quit" {
		return managerAction{kind: "quit"}, true
	}
	if d.settingsEdit {
		switch key {
		case "esc":
			d.settingsEdit = false
		case "enter":
			value := strings.TrimSpace(d.settingsText)
			if d.settingsCursor == 7 && value != "" {
				p, e := strconv.Atoi(value)
				if e != nil || p < 1 || p > 65535 {
					d.settingsError = "Port must be 1–65535, or empty for SSH default"
					return managerAction{}, false
				}
			}
			d.connection[d.settingsCursor-6] = value
			d.settingsEdit = false
			d.settingsError = ""
		case "clear":
			d.settingsText = ""
		case "backspace", "hidden":
			r := []rune(d.settingsText)
			if len(r) > 0 {
				d.settingsText = string(r[:len(r)-1])
			}
		case "space":
			d.settingsText += " "
		default:
			if strings.HasPrefix(key, "text:") {
				d.settingsText += key[5:]
			}
		}
		return managerAction{}, false
	}
	if e, ok := parseMouse(key); ok {
		if e.release {
			return managerAction{}, false
		}
		switch e.button {
		case 63:
			d.settingsCursor--
		case 64:
			d.settingsCursor++
		case 0:
			gutter, width := contentGeometry(w)
			row := e.y - 4 + d.settingsOffset
			if e.x > gutter && e.x <= gutter+width && e.y >= 4 && e.y < 4+max(1, h-8) && row < len(d.settingsRows()) {
				d.settingsCursor = row
				key = "enter"
			}
		}
	}
	switch key {
	case "esc", "text:o":
		d.settings = false
	case "down", "text:j":
		d.settingsCursor++
	case "up", "text:k":
		d.settingsCursor--
	case "enter", "space", "right":
		switch d.settingsCursor {
		case 0:
			d.options.Overwrite = !d.options.Overwrite
		case 1:
			d.options.DryRun = !d.options.DryRun
			d.readonly = d.options.DryRun
		case 2:
			d.options.NoHistory = !d.options.NoHistory
		case 3:
			names := themeOrder
			for i, name := range names {
				if themePreference == name {
					_ = applyTheme(names[(i+1)%len(names)])
					break
				}
			}
		case 4:
			fields := []string{"name", "date", "size", "type"}
			m := d.panes[d.active]
			for i, f := range fields {
				if m.sortOrder.Field == f {
					m.setSort(fields[(i+1)%len(fields)])
					break
				}
			}
		case 5:
			m := d.panes[d.active]
			m.setSort(m.sortOrder.Field)
		case 6, 7, 8, 9, 10:
			d.settingsEdit = true
			d.settingsText = d.connection[d.settingsCursor-6]
			d.settingsError = ""
		case 11:
			target, opts, ok := normalizeTarget(d.connection[0])
			if !ok {
				d.settingsError = "Enter an SSH alias or user@address"
				break
			}
			overrides := []string{}
			for i, flag := range []string{"-p", "-i", "-J", "-F"} {
				if value := d.connection[i+1]; value != "" {
					if flag == "-i" || flag == "-F" {
						value = homeExpand(value)
					}
					overrides = append(overrides, flag, value)
				}
			}
			host := withOverrides(Host{Target: target, Options: opts}, overrides)
			if d.demo {
				d.settingsError = "Connections are disabled in demo mode"
				break
			}
			d.options.SSH = append([]string{}, overrides...)
			d.settings = false
			return managerAction{kind: "connect", host: host}, true
		}
	}
	d.settingsCursor = max(0, min(d.settingsCursor, len(d.settingsRows())-1))
	return managerAction{}, false
}
func (d *dualManager) retryCopy(results chan<- transferResult, replace bool) {
	d.transfer.overwrite = replace || d.options.Overwrite
	d.scanCopy(results)
}
func (d *dualManager) transferInput(key string, results chan<- transferResult) bool {
	t := d.transfer
	if t == nil {
		return false
	}
	if t.busy() && key == "esc" {
		if !t.stopping {
			t.stopping = true
			t.cancel()
			// SFTP requests have no per-request cancellation. Closing this connection
			// also cancels stream helpers; Run reconnects only after the worker exits.
			if d.sftp != nil && (t.action.kind != "delete" || t.action.side == 1) {
				d.sftp.Abort()
			}
		}
		return true
	}
	if buttons := t.buttons(); len(buttons) > 0 && key != "quit" {
		switch key {
		case "left", "up":
			t.button = (t.button + len(buttons) - 1) % len(buttons)
			return true
		case "right", "down", "panel", "tab":
			t.button = (t.button + 1) % len(buttons)
			return true
		case "enter":
			switch buttons[t.button] {
			case "Cancel", "Dismiss":
				key = "esc"
			case "Details":
				d.details, d.detailOffset = t.verb()+" stopped: "+t.message, 0
				return true
			case "Options":
				key = "text:o"
			case "Retry":
				key = "text:r"
			case "Replace":
				d.retryCopy(results, true)
				return true
			default:
				d.executeCopy(results)
				return true
			}
		}
		switch key {
		case "esc":
			if t.cancel != nil {
				t.cancel()
			}
			d.notice = t.verb() + " cancelled"
			if t.stage == "error" {
				d.notice = t.verb() + " stopped · selection kept"
			}
			if t.stage == "conflict" {
				d.notice = "Operation cancelled"
			}
			d.transfer = nil
			return true
		case "text:o":
			d.openSettings()
			return true
		case "text:r":
			if t.stage == "error" && !t.wrote {
				d.retryCopy(results, false)
			}
			return true
		}
		return t.modal()
	}

	return false
}
func (t *panelTransfer) conflictFooter() string {
	return "←/→ Choose   Enter Activate   Esc Cancel"
}
