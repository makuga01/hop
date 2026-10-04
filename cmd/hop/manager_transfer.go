package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type panelTransfer struct {
	button                      int
	stopping, wrote             bool
	scan                        *scanProgress
	overwrite                   bool
	ctx                         context.Context
	cancel                      context.CancelFunc
	removals                    []removalEntry
	action                      managerAction
	destination, stage, message string
	plan                        CopyPlan
	progress                    *batchProgress
}
type transferResult struct {
	removals []removalEntry
	plan     CopyPlan
	err      error
}

func (t *panelTransfer) busy() bool { return t != nil && (t.stage == "scan" || t.stage == "copy") }
func (d *dualManager) beginCopy(a managerAction, results chan<- transferResult) {
	t := &panelTransfer{action: a, destination: d.panes[1-a.side].current, overwrite: d.options.Overwrite}
	d.transfer = t
	d.scanCopy(results)
}
func (d *dualManager) scanCopy(results chan<- transferResult) {
	t := d.transfer
	t.stage = "scan"
	t.stopping, t.wrote = false, false
	t.button = 0
	t.progress = nil
	t.scan = newScanProgress()
	a := t.action
	t.ctx, t.cancel = context.WithCancel(context.Background())
	d.transfer = t
	s := d.sftp
	go func() {
		var p CopyPlan
		var entries []removalEntry
		var e error
		if a.kind == "move" || a.kind == "delete" {
			entries, e = planRemovalObserved(t.ctx, s, a.files, a.side == 1, t.scan)
		}
		if e == nil && a.kind != "delete" {
			if a.kind == "move" {
				t.scan.mu.Lock()
				t.scan.files = 0
				t.scan.dirs = 0
				t.scan.checked = 0
				t.scan.total = 0
				t.scan.mu.Unlock()
			}
			p, e = planBatchObserved(s, a.files, t.destination, a.side == 1, t.overwrite, t.scan)
		}
		results <- transferResult{plan: p, removals: entries, err: e}
	}()
}
func (d *dualManager) executeCopy(results chan<- transferResult) {
	t := d.transfer
	t.stage = "copy"
	t.wrote = true
	t.progress = &batchProgress{embedded: true, total: t.plan.Total, files: len(t.plan.Files), phase: "Preparing folders", started: time.Now(), lastChange: time.Now()}
	if t.action.kind == "delete" {
		t.progress.removing = true
		t.progress.files = len(t.removals)
		t.progress.phase = "Deleting"
	}
	s, h, o := d.sftp, d.host, d.options
	o.Overwrite = t.overwrite
	go func() {
		var e error
		if t.action.kind != "delete" {
			e = executeBatchPlan(s, h, t.plan, t.action.side == 1, o, t.progress)
		}
		if e == nil && (t.action.kind == "move" || t.action.kind == "delete") {
			t.progress.mu.Lock()
			t.progress.finished = false
			t.progress.removing = true
			t.progress.total, t.progress.completed, t.progress.current = 0, 0, 0
			t.progress.files, t.progress.doneFiles = len(t.removals), 0
			t.progress.phase = "Checking sources before deletion"
			t.progress.lastChange = time.Now()
			t.progress.mu.Unlock()
			e = executeRemoval(t.ctx, s, t.removals, t.action.side == 1, t.progress)
			if e != nil && t.action.kind == "move" {
				e = fmt.Errorf("copy completed, but source removal stopped: %w", e)
			}
		}
		if e == nil {
			t.progress.mu.Lock()
			t.progress.finished = true
			t.progress.phase = t.verb() + " complete"
			t.progress.name = ""
			t.progress.mu.Unlock()
		}
		results <- transferResult{err: e}
	}()
}
func (d *dualManager) copyResult(r transferResult, results chan<- transferResult) bool {
	t := d.transfer
	if t.stopping {
		t.stage = "done"
		t.cancel()
		t.progress = nil
		t.message = t.verb() + " stopped · selection kept"
		if t.wrote {
			t.message += " · completed changes are kept"
		}
		d.notice = t.message
		return t.wrote
	}
	if r.err != nil {
		changed := t.stage == "copy"
		t.cancel()
		t.stage = "error"
		t.button = 0
		t.message = r.err.Error()
		var conflict *DestinationExistsError
		if errors.As(r.err, &conflict) {
			t.stage = "conflict"
			t.message = conflict.Path
		}
		return changed
	}
	if t.stage == "scan" {
		t.plan = r.plan
		t.removals = r.removals
		if d.options.DryRun {
			t.cancel()
			t.stage = "done"
			t.message = "Dry run complete · no files changed"
			return false
		}
		if t.action.kind == "delete" || t.action.kind == "move" {
			t.stage = "confirm"
			t.button = 0
			return false
		}
		d.executeCopy(results)
		return false
	}
	t.cancel()
	t.stage = "done"
	t.message = t.verb() + " complete"
	for _, f := range t.action.files {
		if _, ok := d.panes[t.action.side].marks[f.Path]; ok {
			d.panes[t.action.side].toggle(f)
		}
	}
	return true
}
func (d *dualManager) transferLines(w, n int) []string {
	t := d.transfer
	title := t.verb() + " · " + []string{"LOCAL → REMOTE", "REMOTE → LOCAL"}[t.action.side]
	if t.action.kind == "delete" {
		title = "Delete · " + []string{"LOCAL", "REMOTE · " + d.host.Label()}[t.action.side]
	}
	content := []string{}
	switch t.stage {
	case "scan":
		content = append(content, t.scan.lines()...)
		content = append(content, "To: "+t.destination)
		if t.action.kind == "delete" {
			content = append(t.scan.lines(), t.rootSummary())
		}
	case "confirm":
		content = append(content, fmt.Sprintf("%d files · %d folders · %s", len(t.plan.Files), len(t.plan.Directories), humanSize(t.plan.Total)), "To: "+t.destination)
		if t.action.kind == "delete" {
			content = []string{fmt.Sprintf("Permanently delete %d selected items (%d entries)?", len(t.action.files), len(t.removals))}
			available := max(0, n-4)
			for i, f := range t.action.files {
				if i >= available {
					break
				}
				content = append(content, strings.TrimRight(fitEnd(f.Path, w-5), " "))
			}
		}
		if t.action.kind == "move" {
			content = []string{fmt.Sprintf("Move %d files · %s, then remove sources?", len(t.plan.Files), humanSize(t.plan.Total)), "To: " + t.destination}
		}
	case "conflict":
		content = []string{"Existing destination: " + strings.TrimRight(fitEnd(t.message, w-27), " "), "Replace existing files for this batch?"}
	case "error":
		content = append(content, detailLines(t.verb()+" stopped: "+t.message, w-4)...)
		content = append(content, "Details shows full error · Esc dismisses · selection kept")
	default:
		if p := t.progress; p != nil {
			p.mu.Lock()
			percent := p.percent()
			fill := int(percent / 100 * 16)
			content = append(content, fmt.Sprintf("[%s%s] %.0f%% · %d/%d files · %s / %s", strings.Repeat("━", fill), strings.Repeat("·", 16-fill), percent, p.doneFiles, p.files, humanSize(min(p.total, p.completed+p.current)), humanSize(p.total)))
			if p.removing {
				content[len(content)-1] = fmt.Sprintf("[%s%s] %.0f%% · %d/%d entries", strings.Repeat("━", fill), strings.Repeat("·", 16-fill), percent, p.doneFiles, p.files)
			}
			if p.checking {
				content[len(content)-1] = fmt.Sprintf("Checking selection · %d/%d entries · no files deleted yet", p.checked, p.files)
			}
			if !p.removing {
				content = append(content, p.rateText(time.Now()))
			}
			phase := p.phase
			if p.name != "" {
				phase += " · " + p.name
			}
			if !p.finished && time.Since(p.lastChange) > 3*time.Second {
				phase += " · waiting…"
			}
			content = append(content, phase)
			available := n - 2 - len(content)
			if available > 0 {
				content = append(content, p.lines[max(0, len(p.lines)-available):]...)
			}
			p.mu.Unlock()
		} else {
			content = append(content, t.message)
		}
	}
	for len(content) < n-2 {
		content = append(content, "")
	}
	content = content[:n-2]
	if buttons := t.buttons(); len(buttons) > 0 {
		labels := []string{}
		for i, label := range buttons {
			if i == t.button {
				labels = append(labels, "> "+label+" <")
			} else {
				labels = append(labels, "[ "+label+" ]")
			}
		}
		content[len(content)-1] = " " + strings.Join(labels, "   ")
	}

	lines := []string{uiRule(title, w, true)}
	for i, line := range content {
		style := uiBase
		if t.stage == "error" {
			style = uiError
		}
		rendered := uiBoxLine(" "+line, w, style)
		if buttons := t.buttons(); len(buttons) > 0 && i == len(content)-1 {
			label := "> " + buttons[t.button] + " <"
			rendered = strings.Replace(rendered, label, uiPaint(label, uiSelected)+uiPaint("", uiBase), 1)
		}
		lines = append(lines, rendered)
	}
	return append(lines, uiBottom(w))
}

func (t *panelTransfer) verb() string {
	switch t.action.kind {
	case "move":
		return "Move"
	case "delete":
		return "Delete"
	}
	return "Copy"
}
func (t *panelTransfer) rootSummary() string {
	names := []string{}
	for _, f := range t.action.files {
		names = append(names, safeText(f.Path))
	}
	return strings.Join(names, " · ")
}

func (t *panelTransfer) modal() bool {
	return t != nil && (t.busy() || t.stage == "confirm" || t.stage == "conflict" || t.stage == "error")
}

func (t *panelTransfer) buttons() []string {
	switch t.stage {
	case "confirm":
		return []string{t.verb(), "Cancel"}
	case "conflict":
		return []string{"Replace", "Cancel", "Options"}
	case "error":
		if t.wrote {
			return []string{"Details", "Options", "Dismiss"}
		}
		return []string{"Retry", "Options", "Dismiss", "Details"}
	}
	return nil
}
func (t *panelTransfer) buttonAt(x int) int {
	start := 3
	for i, label := range t.buttons() {
		width := textWidth(label) + 4
		if x >= start && x < start+width {
			return i
		}
		start += width + 3
	}
	return -1
}
