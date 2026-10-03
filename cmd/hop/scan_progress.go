package main

import (
	"fmt"
	"sync"
	"time"
)

type scanProgress struct {
	mu                             sync.Mutex
	phase                          string
	files, dirs, checked, total    int
	started, changed, phaseStarted time.Time
	durations                      map[string]time.Duration
}

func newScanProgress() *scanProgress {
	now := time.Now()
	return &scanProgress{started: now, changed: now, phaseStarted: now, durations: map[string]time.Duration{}}
}
func (p *scanProgress) setPhase(phase string, total int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if p.phase != "" {
		p.durations[p.phase] += now.Sub(p.phaseStarted)
	}
	p.phase = phase
	p.total = total
	p.changed = now
	p.phaseStarted = now
}
func (p *scanProgress) found(dir bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if dir {
		p.dirs++
	} else {
		p.files++
	}
	p.changed = time.Now()
}
func (p *scanProgress) checkedFile() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checked++
	p.changed = time.Now()
}
func (p *scanProgress) lines() []string {
	if p == nil {
		return []string{"Scanning selection and checking destinations…"}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	phase := p.phase
	if phase == "" {
		phase = "Checking selection"
	}
	phase += fmt.Sprintf(" · %ds", int(time.Since(p.started).Seconds()))
	if time.Since(p.changed) >= 3*time.Second {
		phase += " · waiting for server…"
	}
	detail := fmt.Sprintf("%d files · %d folders found", p.files, p.dirs)
	if p.total > 0 {
		detail = fmt.Sprintf("%d / %d files checked · %.0f%%", p.checked, p.total, 100*float64(p.checked)/float64(p.total))
	}
	return []string{phase, detail}
}
