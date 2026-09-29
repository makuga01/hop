package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type panelTransfer struct {
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
func longCopy(p CopyPlan) bool {
	return p.Total >= 256*1024*1024 || len(p.Files)+len(p.Directories) >= 200
}
func (d *dualManager) beginCopy(a managerAction, results chan<- transferResult) {
	t := &panelTransfer{action: a, destination: d.panes[1-a.side].current, overwrite: d.options.Overwrite}
	d.transfer = t
	d.scanCopy(results)
}
func (d *dualManager) scanCopy(results chan<- transferResult) {
	t := d.transfer
	t.stage = "scan"
	t.progress = nil
	a := t.action
	t.ctx, t.cancel = context.WithCancel(context.Background())
	d.transfer = t
	s := d.sftp
	go func() {
		var p CopyPlan
		var entries []removalEntry
		var e error
		if a.kind == "move" || a.kind == "delete" {
			entries, e = planRemoval(t.ctx, s, a.files, a.side == 1)
		}
		if e == nil && a.kind != "delete" {
			p, e = planBatch(s, a.files, t.destination, a.side == 1, t.overwrite)
		}
		results <- transferResult{plan: p, removals: entries, err: e}
	}()
}
func (d *dualManager) executeCopy(results chan<- transferResult) {
	t := d.transfer
	t.stage = "copy"
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
			t.progress.phase = "Removing sources"
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
	if r.err != nil {
		changed := t.stage == "copy"
		t.cancel()
		t.stage = "error"
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
		if t.action.kind == "delete" || t.action.kind == "move" || (longCopy(r.plan) && !d.options.Yes) {
			t.stage = "confirm"
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
		content = append(content, "Scanning selection and checking destinations…", "To: "+t.destination)
		if t.action.kind == "delete" {
			content = []string{"Scanning selection for deletion…", t.rootSummary()}
		}
	case "confirm":
		content = append(content, fmt.Sprintf("%d files · %d folders · %s — this may take a while", len(t.plan.Files), len(t.plan.Directories), humanSize(t.plan.Total)), "To: "+t.destination)
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
		content = append(content, t.verb()+" stopped: "+t.message, "Esc dismiss · selection kept")
	default:
		if p := t.progress; p != nil {
			p.mu.Lock()
			percent := p.percent()
			fill := int(percent / 100 * 16)
			content = append(content, fmt.Sprintf("[%s%s] %.0f%% · %d/%d files · %s / %s", strings.Repeat("━", fill), strings.Repeat("·", 16-fill), percent, p.doneFiles, p.files, humanSize(min(p.total, p.completed+p.current)), humanSize(p.total)))
			if p.removing {
				content[len(content)-1] = fmt.Sprintf("[%s%s] %.0f%% · %d/%d entries", strings.Repeat("━", fill), strings.Repeat("·", 16-fill), percent, p.doneFiles, p.files)
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
	if t.stage == "conflict" {
		content[len(content)-1] = " [ Replace ]   [ Cancel ]   [ Options ]"
	}
	if t.stage == "error" {
		content[len(content)-1] = " [ Retry ]   [ Options ]   [ Dismiss ]"
	}
	if t.stage == "confirm" {
		content[len(content)-1] = " [ " + fit(t.verb(), 8) + " ]   [ Cancel ]"
	}
	lines := []string{uiRule(title, w, true)}
	for _, line := range content {
		style := uiBase
		if t.stage == "error" {
			style = uiError
		}
		lines = append(lines, uiBoxLine(" "+line, w, style))
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
	return t != nil && (t.busy() || t.stage == "confirm" || t.stage == "conflict")
}
