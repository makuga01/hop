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
	// Keep a longer history for ETA; the displayed speed remains responsive.
	for len(m.samples) > 2 && now.Sub(m.samples[1].at) >= 20*time.Second {
		m.samples = m.samples[1:]
	}
	first := m.samples[0]
	recent := first
	for _, sample := range m.samples {
		if now.Sub(sample.at) >= 5*time.Second {
			recent = sample
		} else {
			break
		}
	}
	seconds := now.Sub(recent.at).Seconds()
	rate := 0.0
	if seconds >= 1 {
		rate = float64(bytes-recent.bytes) / seconds
	}
	eta := "—"
	elapsed := now.Sub(first.at).Seconds()
	remaining := 0.0
	known := elapsed >= 3
	if bytes < total {
		average := float64(bytes-first.bytes) / math.Max(elapsed, 1)
		// File completions are not a clock: a large file can occupy the stream
		// while thousands of tiny files remain. Never extrapolate that file count
		// over a byte transfer. Suppress estimates during stalls or sharp bursts.
		known = known && average > 0 && rate > 0 && float64(bytes) >= float64(total)*.01
		if known {
			known = rate/average >= .25 && rate/average <= 4
			remaining = float64(total-bytes) / average
		}
	} else if total == 0 && count > files {
		fileRate := float64(files-first.files) / math.Max(elapsed, 1)
		known = known && fileRate > 0 && files > recent.files
		if known {
			remaining = float64(count-files) / fileRate
		}
	}
	if known && remaining > 0 {
		eta = "~" + transferDuration(remaining)
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
