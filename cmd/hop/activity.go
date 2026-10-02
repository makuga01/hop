package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

func colorEnabled() bool {
	_, off := os.LookupEnv("NO_COLOR")
	return !off && os.Getenv("TERM") != "dumb"
}
func styled(s, style string, color bool) string {
	if !color || style == "" {
		return s
	}
	return "\x1b[" + style + "m" + s + "\x1b[0m"
}

// A heartbeat runs independently of SFTP replies, including OPEN, CLOSE and
// RENAME. Silence from a server never looks like an unacknowledged confirmation.
type activity struct {
	file              *fileProgress
	mu                sync.Mutex
	parent            *batchProgress
	out               io.Writer
	live, color       bool
	phase             string
	bytes, total      uint64
	transfer          bool
	start, lastChange time.Time
	stop, done        chan struct{}
	once              sync.Once
}

func startActivity(phase string, total uint64, transfer bool) *activity {
	if activeScreen != nil {
		activeScreen.Page(phase)
		return newActivity(screenStatusWriter{activeScreen}, true, false, phase, total, transfer)
	}
	_, e := stty(os.Stderr, "-g")
	return newActivity(os.Stderr, e == nil, colorEnabled() && e == nil, phase, total, transfer)
}
func newActivity(out io.Writer, live, color bool, phase string, total uint64, transfer bool) *activity {
	now := time.Now()
	a := &activity{out: out, live: live, color: color, phase: phase, total: total, transfer: transfer, start: now, lastChange: now, stop: make(chan struct{}), done: make(chan struct{})}
	a.render(0)
	go func() {
		defer close(a.done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			select {
			case <-a.stop:
				return
			case <-ticker.C:
				frame++
				if a.live || frame%25 == 0 {
					a.render(frame)
				}
			}
		}
	}()
	return a
}
func (a *activity) text(now time.Time, frame int) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	msg := frames[frame%len(frames)] + " " + a.phase
	if a.transfer {
		percent := float64(0)
		if a.total > 0 {
			percent = 100 * float64(a.bytes) / float64(a.total)
		}
		msg += fmt.Sprintf(" · %.0f%% · %s / %s", percent, humanSize(a.bytes), humanSize(a.total))
	}
	msg += fmt.Sprintf(" · %ds", int(now.Sub(a.start).Seconds()))
	if now.Sub(a.lastChange) >= 3*time.Second {
		msg += " · waiting…"
	}
	return msg
}
func (a *activity) render(frame int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	msg := a.text(time.Now(), frame)
	if a.live {
		fmt.Fprint(a.out, "\r\x1b[2K", styled(msg, uiMuted, a.color))
	} else {
		fmt.Fprintln(a.out, msg)
	}
}
func (a *activity) Phase(phase string) {
	if a.parent != nil {
		a.parent.mu.Lock()
		a.parent.phase = phase
		a.parent.lastChange = time.Now()
		a.parent.mu.Unlock()
		return
	}
	a.mu.Lock()
	a.phase = phase
	a.lastChange = time.Now()
	a.mu.Unlock()
	a.render(0)
}
func (a *activity) Update(n uint64) {
	if a.file != nil {
		a.file.update(n)
		return
	}
	if a.parent != nil {
		a.parent.mu.Lock()
		if a.transfer {
			a.parent.current = min(n, a.parent.currentSize)
			a.parent.lastChange = time.Now()
		}
		a.parent.mu.Unlock()
		return
	}
	a.mu.Lock()
	a.bytes = n
	a.lastChange = time.Now()
	a.mu.Unlock()
}
func (a *activity) Stop() {
	if a.parent != nil {
		return
	}
	a.once.Do(func() {
		close(a.stop)
		<-a.done
		if a.live {
			fmt.Fprint(a.out, "\r\x1b[2K")
		}
	})
}
