package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func protocolFixture(t *testing.T, respond func(byte, []byte) (byte, []byte)) *SFTP {
	t.Helper()
	toR, toW := io.Pipe()
	fromR, fromW := io.Pipe()
	s := &SFTP{in: toW, out: fromR, timeout: time.Second}
	server := &SFTP{in: fromW, out: toR}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			typ, b, err := server.receive()
			if err != nil {
				return
			}
			typ, b = respond(typ, b)
			if err := server.packet(typ, b); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { s.Close(); toR.Close(); fromR.Close(); fromW.Close(); <-done })
	return s
}

func TestPipelineShortReads(t *testing.T) {
	payload := bytes.Repeat([]byte("short read\x00"), 300000)
	s := protocolFixture(t, func(typ byte, b []byte) (byte, []byte) {
		d := decoder{b: b}
		id := d.u32()
		d.str()
		off, n := d.u64(), d.u32()
		n = min(n, 997)
		return fxData, fields(u32(id), u32(n), payload[off:off+uint64(n)])
	})
	f, err := os.CreateTemp(t.TempDir(), "short")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var previous uint64
	if err := s.downloadData("handle", f, uint64(len(payload)), func(n uint64) {
		if n < previous || n > uint64(len(payload)) {
			t.Errorf("invalid progress %d after %d", n, previous)
		}
		previous = n
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(f.Name())
	if !bytes.Equal(got, payload) || previous != uint64(len(payload)) {
		t.Fatal("short reads corrupted download")
	}
}

func TestPipelineWriteErrorDrainsReplies(t *testing.T) {
	writes := 0
	s := protocolFixture(t, func(typ byte, b []byte) (byte, []byte) {
		d := decoder{b: b}
		id := d.u32()
		if typ == fxWrite {
			writes++
			code := uint32(0)
			if writes == 3 {
				code = 4
			}
			return fxStatus, fields(u32(id), u32(code), str("fixture"), str(""))
		}
		return fxAttrs, fields(u32(id), u32(5), u64(42), u32(0100600))
	})
	err := s.uploadData(bytes.NewReader(make([]byte, 4<<20)), "handle", 4<<20, nil)
	var status *StatusError
	if !errors.As(err, &status) {
		t.Fatalf("wanted write failure: %v", err)
	}
	a, err := s.Stat("still-usable", true)
	if err != nil || a.Size != 42 {
		t.Fatalf("connection lost synchronization: %+v %v", a, err)
	}
}

type observedWriter struct {
	io.WriteCloser
	started chan struct{}
	once    sync.Once
}

func (w *observedWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return w.WriteCloser.Write(b)
}
func TestAbortWakesConcurrentRequests(t *testing.T) {
	toR, toW := io.Pipe()
	fromR, fromW := io.Pipe()
	w := &observedWriter{WriteCloser: toW, started: make(chan struct{})}
	s := &SFTP{in: w, out: fromR, timeout: time.Hour}
	defer toR.Close()
	defer fromR.Close()
	defer fromW.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Stat("blocked", true); err == nil {
				t.Error("request succeeded after abort")
			}
		}()
	}
	// No server consumes input: Abort must also interrupt a blocked packet write.
	<-w.started
	s.Abort()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked requests did not wake")
	}
}

func TestParallelBatchRoundTrip(t *testing.T) {
	s := localSFTP(t)
	old := dataDir
	dataDir = t.TempDir()
	defer func() { dataDir = old }()
	source, remote, dest := t.TempDir(), t.TempDir(), t.TempDir()
	for i := 0; i < 40; i++ {
		payload := bytes.Repeat([]byte{byte(i)}, i*137)
		if i == 0 {
			payload = bytes.Repeat([]byte("large"), 600000)
		}
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("%02d ' $ file", i)), payload, 0640); err != nil {
			t.Fatal(err)
		}
	}
	for _, get := range []bool{false, true} {
		src, dst := source, remote
		if get {
			src, dst = remote, dest
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		var roots []FileItem
		for _, e := range entries {
			roots = append(roots, FileItem{Path: filepath.Join(src, e.Name()), Regular: true})
		}
		plan, err := planBatch(s, roots, dst, get, false)
		if err != nil {
			t.Fatal(err)
		}
		p := &batchProgress{embedded: true, total: plan.Total, files: len(plan.Files)}
		if err := executeBatchPlan(s, Host{Target: "fixture"}, plan, get, Options{}, p); err != nil {
			t.Fatal(err)
		}
		if p.doneFiles != 40 || p.current != 0 || p.completed != plan.Total {
			t.Fatalf("wrong progress: done=%d current=%d total=%d", p.doneFiles, p.current, p.completed)
		}
		for _, entry := range entries {
			want, _ := os.ReadFile(filepath.Join(source, entry.Name()))
			got, err := os.ReadFile(filepath.Join(dst, entry.Name()))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("corrupted %s: %v", entry.Name(), err)
			}
		}
		leftovers, _ := filepath.Glob(filepath.Join(dst, ".hop-*.partial"))
		if len(leftovers) > 0 {
			t.Fatal(leftovers)
		}
	}
	state, err := readState()
	if err != nil || len(state.Transfers) != 80 {
		t.Fatalf("history: %d entries, %v", len(state.Transfers), err)
	}
}

func TestParallelFilesStopsAndJoinsOnFailure(t *testing.T) {
	var mu sync.Mutex
	started, finished := 0, 0
	sentinel := errors.New("fixture failure")
	err := parallelFiles(1000, func(i int) error {
		mu.Lock()
		started++
		mu.Unlock()
		defer func() { mu.Lock(); finished++; mu.Unlock() }()
		return sentinel
	})
	if !errors.Is(err, sentinel) || started > batchWorkers || started != finished {
		t.Fatalf("err=%v started=%d finished=%d", err, started, finished)
	}
}

func TestUnexpectedReplyFailsAllWaiters(t *testing.T) {
	s := protocolFixture(t, func(typ byte, b []byte) (byte, []byte) {
		return fxStatus, fields(u32(0), u32(0), str(""), str(""))
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Stat("fixture", true); err == nil {
				t.Error("unexpected response accepted")
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waiters stuck after invalid reply")
	}
}
