package main

import (
	"fmt"
	"math"
	"time"
)

type transferSample struct {
	at    time.Time
	bytes uint64
	files int
}

// Owned by the enclosing progress mutex. Bytes include locally reused data,
// so this measures effective throughput, not network traffic.
type transferMeter struct{ samples []transferSample }

func (m *transferMeter) text(now, started time.Time, bytes, total uint64, files, count int, finished bool) string {
	current := transferSample{now, bytes, files}
	if len(m.samples) == 0 {
		m.samples = append(m.samples, transferSample{at: started})
	}
	last := m.samples[len(m.samples)-1]
	if bytes < last.bytes || files < last.files {
		m.samples = []transferSample{current}
	}
	if now.Sub(m.samples[len(m.samples)-1].at) >= 200*time.Millisecond {
		m.samples = append(m.samples, current)
	}
	for len(m.samples) > 2 && now.Sub(m.samples[1].at) >= 5*time.Second {
		m.samples = m.samples[1:]
	}
	first := m.samples[0]
	seconds := now.Sub(first.at).Seconds()
	rate, fileRate := 0.0, 0.0
	if seconds >= 1 {
		rate = float64(bytes-first.bytes) / seconds
		fileRate = float64(files-first.files) / seconds
	}
	eta := "—"
	remaining := 0.0
	known := seconds >= 1
	if bytes < total {
		if rate > 0 {
			remaining = float64(total-bytes) / rate
		} else {
			known = false
		}
	}
	if count > files {
		if fileRate > 0 {
			remaining = math.Max(remaining, float64(count-files)/fileRate)
		} else if bytes >= total {
			known = false
		}
	}
	if known && remaining > 0 {
		eta = transferDuration(remaining)
	}
	if finished {
		eta = "0s"
	}
	return fmt.Sprintf("%.1f MB/s effective · ETA %s", rate/1e6, eta)
}

func transferDuration(seconds float64) string {
	// Cap an unstable estimate before converting it to an integer.
	if seconds >= 86400 {
		return ">24h"
	}
	n := int(math.Ceil(seconds))
	if n >= 3600 {
		return fmt.Sprintf("%dh %dm", n/3600, n%3600/60)
	}
	if n >= 60 {
		return fmt.Sprintf("%dm %ds", n/60, n%60)
	}
	return fmt.Sprintf("%ds", n)
}

func (p *batchProgress) rateText(now time.Time) string {
	text := p.meter.text(now, p.started, min(p.total, p.completed+p.current), p.total, p.doneFiles, p.files, p.finished)
	if p.backend != "" {
		text += " · " + p.backend
	}
	return text
}
