package main

import (
	"strings"
	"testing"
	"time"
)

func TestTransferMeter(t *testing.T) {
	start := time.Unix(100, 0)
	var m transferMeter
	check := func(at time.Duration, bytes uint64, files int, finished bool, want string) {
		t.Helper()
		got := m.text(start.Add(at), start, bytes, 12e6, files, 6, finished)
		if !strings.Contains(got, want) {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	check(0, 0, 0, false, "ETA —")
	check(time.Second, 2e6, 1, false, "2.0 MB/s effective · ETA —")
	check(2*time.Second, 4e6, 2, false, "ETA —")
	check(3*time.Second, 6e6, 3, false, "ETA ~3s")
	for i := 4; i <= 9; i++ {
		m.text(start.Add(time.Duration(i)*time.Second), start, 6e6, 12e6, 3, 6, false)
	}
	check(10*time.Second, 6e6, 3, false, "0.0 MB/s effective · ETA —")
	check(11*time.Second, 1e6, 0, false, "ETA —")  // retry resets counters
	check(12*time.Second, 12e6, 5, false, "ETA —") // new sampling window after retry
	check(13*time.Second, 12e6, 6, true, "ETA 0s")
}
func TestTransferMeterEmptyFilesAndFinalization(t *testing.T) {
	start := time.Unix(100, 0)
	var m transferMeter
	m.text(start, start, 0, 0, 0, 10, false)
	got := m.text(start.Add(4*time.Second), start, 0, 0, 2, 10, false)
	if !strings.Contains(got, "ETA ~16s") {
		t.Fatal(got)
	}
	var single transferMeter
	single.text(start, start, 0, 100, 0, 0, false)
	got = single.text(start.Add(time.Second), start, 100, 100, 0, 0, false)
	if !strings.Contains(got, "ETA —") {
		t.Fatal(got)
	}
	for i := 0; i < 1000; i++ {
		m.text(start.Add(time.Duration(i)*200*time.Millisecond), start, 0, 0, 2, 10, false)
	}
	if len(m.samples) > 103 {
		t.Fatalf("unbounded samples: %d", len(m.samples))
	}
}
func TestScanAndTransferPanelProgress(t *testing.T) {
	scan := newScanProgress()
	scan.setPhase("Discovering files and folders", 0)
	scan.found(false)
	scan.found(true)
	if got := scan.lines()[1]; got != "1 files · 1 folders found" {
		t.Fatal(got)
	}
	scan.setPhase("Checking destinations and read access", 4)
	scan.checkedFile()
	if got := scan.lines()[1]; got != "1 / 4 files checked · 25%" {
		t.Fatal(got)
	}
	d := &dualManager{transfer: &panelTransfer{stage: "scan", scan: scan}}
	if got := strings.Join(d.transferLines(80, 7), "\n"); !strings.Contains(got, "25%") {
		t.Fatal(got)
	}
	d.transfer.stage = "copy"
	d.transfer.progress = &batchProgress{started: time.Now(), total: 100, files: 1}
	for _, height := range []int{4, 7} {
		got := strings.Join(d.transferLines(80, height), "\n")
		if !strings.Contains(got, "MB/s effective") || !strings.Contains(got, "ETA") {
			t.Fatal(got)
		}
	}
}

func TestTransferMeterLargeFileDoesNotExtrapolateFileCount(t *testing.T) {
	start := time.Unix(100, 0)
	var m transferMeter
	m.text(start, start, 0, 400e6, 0, 23541, false)
	got := m.text(start.Add(5*time.Second), start, 50e6, 400e6, 1, 23541, false)
	if !strings.Contains(got, "ETA ~35s") {
		t.Fatal(got)
	}
	// Once all bytes arrived, don't apply the old file-count extrapolation
	// to final commits either.
	got = m.text(start.Add(6*time.Second), start, 400e6, 400e6, 2, 23541, false)
	if !strings.Contains(got, "ETA —") {
		t.Fatal(got)
	}
	// Tiny initial samples and a stalled stream cannot justify a huge ETA.
	var warmup transferMeter
	warmup.text(start, start, 0, 400e6, 0, 23541, false)
	got = warmup.text(start.Add(5*time.Second), start, 1024, 400e6, 1, 23541, false)
	if !strings.Contains(got, "ETA —") {
		t.Fatal(got)
	}
	for i := 6; i <= 12; i++ {
		got = m.text(start.Add(time.Duration(i)*time.Second), start, 400e6, 400e6, 2, 23541, false)
	}
	if !strings.Contains(got, "0.0 MB/s effective · ETA —") {
		t.Fatal(got)
	}
}

func TestTransferMeterBackendChange(t *testing.T) {
	p := &batchProgress{backend: "Stream", total: 400e6, completed: 300e6, files: 100, doneFiles: 90}
	p.meter.samples = []transferSample{{at: time.Now().Add(-10 * time.Second)}}
	p.setBackend("Delta")
	if len(p.meter.samples) != 1 || p.meter.samples[0].bytes != 300e6 || p.meter.samples[0].files != 90 {
		t.Fatal("backend did not reset sample baseline")
	}
	if got := p.rateText(time.Now()); !strings.Contains(got, "ETA —") {
		t.Fatal(got)
	}
}
