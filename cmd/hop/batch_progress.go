package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// One progress owner for the whole batch; file activities only update its state.
type batchProgress struct {
	backend                                string
	meter                                  transferMeter
	console                                *activity
	removing                               bool
	checking                               bool
	checked                                int
	embedded                               bool
	lines                                  []string
	mu                                     sync.Mutex
	screen                                 *fullScreen
	total, completed, current, currentSize uint64
	files, doneFiles                       int
	phase, name                            string
	started, lastChange                    time.Time
	finished                               bool
	stop, done                             chan struct{}
	once                                   sync.Once
}

func startBatchProgress(screen *fullScreen, plan CopyPlan, h Host) *batchProgress {
	if screen == nil {
		return nil
	}
	screen.TransferPage(h.Label(), plan.Destination)
	p := &batchProgress{screen: screen, total: plan.Total, files: len(plan.Files), phase: "Preparing folders", started: time.Now(), lastChange: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	p.render()
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				p.render()
			}
		}
	}()
	return p
}
func (p *batchProgress) percent() float64 {
	if p.finished {
		return 100
	}
	if p.total > 0 {
		return min(99, 100*float64(p.completed+p.current)/float64(p.total))
	}
	if p.files > 0 {
		return min(99, 100*float64(p.doneFiles)/float64(p.files))
	}
	return 0
}
func (p *batchProgress) render() {
	if p.embedded {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.screen
	s.mu.Lock()
	defer s.mu.Unlock()
	percent := p.percent()
	gutter, width := contentGeometry(s.width)
	filled := int(percent / 100 * float64(width))
	elapsed := int(time.Since(p.started).Seconds())
	bytes := fmt.Sprintf("%s / %s", humanSize(min(p.total, p.completed+p.current)), humanSize(p.total))
	summary := uiSpans(width, uiBase, uiSpan{fit("OVERALL PROGRESS", max(0, width-7)), uiMuted}, uiSpan{fitRight(fmt.Sprintf("%.0f%%", percent), 7), uiFolder})
	bar := uiPaint(strings.Repeat("━", filled), uiFolder) + uiPaint(strings.Repeat("━", width-filled), uiHeader)
	stats := fmt.Sprintf("%d/%d files · %s", p.doneFiles, p.files, bytes)
	stats = fit(stats, max(0, width-8)) + fmt.Sprintf("   %02d:%02d", elapsed/60, elapsed%60)
	phase := p.phase
	if !p.removing {
		phase = p.rateText(time.Now()) + " · " + phase
	}
	if !p.finished && time.Since(p.lastChange) >= 3*time.Second {
		phase += " · waiting…"
	}
	if p.name != "" {
		phase += " · " + p.name
	}
	fmt.Fprintf(s.tty, "\x1b7\x1b[5;%dH%s\x1b[6;%dH%s\x1b[7;%dH%s\x1b[8;%dH%s\x1b8", gutter+1, summary, gutter+1, bar, gutter+1, uiPaint(fit(stats, width), uiMuted), gutter+1, uiPaint(fit(phase, width), uiAccent))
}
func (p *batchProgress) activity(phase string, total uint64, transfer bool) *activity {
	p.mu.Lock()
	p.phase = phase
	p.lastChange = time.Now()
	p.mu.Unlock()
	return &activity{parent: p, transfer: transfer, total: total}
}
func (o Options) activity(phase string, total uint64, transfer bool) *activity {
	if o.Progress != nil {
		a := o.Progress.activity(phase, total, transfer)
		a.file = o.fileProgress
		return a
	}
	return startActivity(phase, total, transfer)
}
func (p *batchProgress) log(line string, replace bool) {
	if p.console != nil {
		fmt.Fprintln(os.Stderr, line)
		return
	}
	if p.embedded {
		if replace && len(p.lines) > 0 {
			p.lines[len(p.lines)-1] = line
		} else {
			p.lines = append(p.lines, line)
		}
		if len(p.lines) > 200 {
			p.lines = p.lines[len(p.lines)-200:]
		}
		return
	}
	s := p.screen
	s.mu.Lock()
	defer s.mu.Unlock()
	gutter, width := contentGeometry(s.width)
	style := uiBase
	if strings.HasPrefix(line, " ✓") {
		style = uiFolder
	}
	if strings.HasPrefix(line, " →") {
		style = uiAccent
	}
	if strings.HasPrefix(line, " ×") {
		style = uiError
	}
	rendered := uiPaint(strings.Repeat(" ", gutter), uiBase) + uiPaint(fit(line, width), style) + uiPaint(strings.Repeat(" ", max(0, s.width-1-width-gutter)), uiBase)
	if replace {
		fmt.Fprint(s.tty, "\x1b7\x1b[1A\r", rendered, "\x1b8")
	} else {
		fmt.Fprint(s.tty, rendered, uiBaseSequence(), "\r\n")
	}
}
func (p *batchProgress) beginFile(index int, f PlannedFile, get bool, dest string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	name := f.Remote
	if get {
		name = f.Local
	}
	if rel, e := filepath.Rel(dest, name); e == nil {
		name = rel
	}
	p.name = name
	p.current = 0
	p.currentSize = f.Size
	p.phase = "Checking file"
	p.lastChange = time.Now()
	p.log(fmt.Sprintf(" → [%d/%d] %s", index+1, p.files, safeText(name)), false)
}
func (p *batchProgress) completeFile(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.phase = "Failed"
		p.log(" × "+safeText(p.name)+" · "+safeText(err.Error()), true)
		return
	}
	p.completed += p.currentSize
	p.current = 0
	p.doneFiles++
	p.log(" ✓ "+safeText(p.name), true)
}
func (p *batchProgress) folder(name string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastChange = time.Now()
	p.log(" + "+safeText(name)+"/", false)
}
func (p *batchProgress) finish() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.finished = true
	p.phase = "Copy complete"
	p.name = ""
	p.mu.Unlock()
	p.render()
}
func (p *batchProgress) Stop() {
	if p == nil || p.embedded {
		return
	}
	p.once.Do(func() { close(p.stop); <-p.done; p.render() })
}

func (o Options) warning(message string) {
	if o.Progress != nil && o.Progress.embedded {
		p := o.Progress
		p.mu.Lock()
		p.log(" ! "+safeText(message), false)
		p.mu.Unlock()
		return
	}
	fmt.Fprintln(os.Stderr, message)
}

// Each concurrent file contributes a delta to the shared progress total.
type fileProgress struct {
	parent        *batchProgress
	name          string
	size, current uint64
}

func (p *batchProgress) startFile(f PlannedFile, get bool, dest string) *fileProgress {
	name := f.Remote
	if get {
		name = f.Local
	}
	if rel, err := filepath.Rel(dest, name); err == nil {
		name = rel
	}
	p.mu.Lock()
	p.name = name
	p.lastChange = time.Now()
	p.mu.Unlock()
	return &fileProgress{parent: p, name: name, size: f.Size}
}
func (f *fileProgress) update(n uint64) {
	p := f.parent
	p.mu.Lock()
	defer p.mu.Unlock()
	n = min(n, f.size)
	p.current -= f.current
	p.current += n
	f.current = n
	p.lastChange = time.Now()
	if p.console != nil {
		p.console.Update(p.completed + p.current)
	}
}
func (f *fileProgress) complete(err error) {
	p := f.parent
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current -= f.current
	if err != nil {
		p.log(" × "+safeText(f.name)+" · "+safeText(err.Error()), false)
	} else {
		p.completed += f.size
		p.doneFiles++
		p.log(" ✓ "+safeText(f.name), false)
	}
	p.lastChange = time.Now()
	if p.console != nil {
		p.console.Update(p.completed + p.current)
	}
}

func (p *batchProgress) setBackend(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.backend != name {
		// A new transfer method has a different cost model (e.g. stream -> delta).
		p.meter.samples = []transferSample{{at: time.Now(), bytes: min(p.total, p.completed+p.current), files: p.doneFiles}}
	}
	p.backend = name
	p.phase = "Copying files"
	p.lastChange = time.Now()
	if p.console != nil {
		p.console.Phase("Copying files · " + name)
	}
}
