package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestServerLimitsGeometry(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		limits               transferLimits
		read, write, workers int
	}{
		{"conservative", transferLimits{}, 32768, 32768, 8},
		{"openssh", transferLimits{packet: 262144, read: 261120, write: 261120, handles: 1024, negotiated: true}, 261120, 261120, 32},
		{"small", transferLimits{packet: 16384, read: 8192, write: 4096, handles: 6, negotiated: true}, 8192, 4096, 3},
		{"unlimited", transferLimits{negotiated: true}, 262144, 262144, 32},
		{"packet-overhead", transferLimits{packet: 34000, negotiated: true}, 33987, 33969, 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &SFTP{limits: tc.limits}
			s.limitsOnce.Do(func() {})
			for _, upload := range []bool{false, true} {
				n, w, err := s.transferGeometry("handle", upload)
				expected := tc.read
				if upload {
					expected = tc.write
				}
				if err != nil || n != expected || w > 256 || n*w > 8<<20 {
					t.Fatalf("chunk=%d window=%d err=%v", n, w, err)
				}
			}
			workers, err := s.fileWorkers()
			if err != nil || workers != tc.workers {
				t.Fatal(workers, err)
			}
		})
	}
}
func TestLimitsNegotiatedOnlyOnce(t *testing.T) {
	var requests atomic.Int32
	s := protocolFixture(t, func(typ byte, b []byte) (byte, []byte) {
		requests.Add(1)
		id := binary.BigEndian.Uint32(b)
		return 201, fields(u32(id), u64(262144), u64(261120), u64(261120), u64(128))
	})
	s.ext = map[string]string{"limits@openssh.com": "1"}
	if err := parallelFiles(100, func(i int) error { _, _, err := s.transferGeometry("handle", i%2 == 0); return err }); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal(requests.Load())
	}
}
func TestLimitsUnsupportedAndMalformed(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		s := protocolFixture(t, func(typ byte, b []byte) (byte, []byte) {
			id := binary.BigEndian.Uint32(b)
			if malformed {
				return 201, fields(u32(id), u64(1))
			}
			return fxStatus, fields(u32(id), u32(8), str("unsupported"), str(""))
		})
		s.ext = map[string]string{"limits@openssh.com": "1"}
		n, w, err := s.transferGeometry("handle", true)
		if malformed {
			if err == nil {
				t.Fatal("accepted malformed limits")
			}
		} else if err != nil || n != 32768 || w != 64 {
			t.Fatal(n, w, err)
		}
	}
}

type shortPacketWriter struct{ bytes.Buffer }

func (w *shortPacketWriter) Write(b []byte) (int, error) { return w.Buffer.Write(b[:min(7, len(b))]) }
func (w *shortPacketWriter) Close() error                { return nil }
func TestPacketEncodingHandlesShortWrites(t *testing.T) {
	w := &shortPacketWriter{}
	s := &SFTP{in: w}
	payload := bytes.Repeat([]byte("payload"), 100000)
	if err := s.packet(fxWrite, u32(73), payload); err != nil {
		t.Fatal(err)
	}
	server := &SFTP{out: bytes.NewReader(w.Bytes())}
	typ, b, err := server.receive()
	if err != nil || typ != fxWrite || !bytes.Equal(b, fields(u32(73), payload)) {
		t.Fatal("packet corrupted", err)
	}
}
func TestBatchExactDestinationRejectsNewDirectory(t *testing.T) {
	s := localSFTP(t)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, get := range []bool{false, true} {
		dest := t.TempDir()
		plan, err := planBatch(s, []FileItem{{Path: source, Regular: true}}, dest, get, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dest, "source"), 0700); err != nil {
			t.Fatal(err)
		}
		p := &batchProgress{embedded: true, total: plan.Total, files: 1}
		if err := executeBatchPlan(s, Host{Target: "fixture"}, plan, get, Options{}, p); err == nil {
			t.Fatal("accepted new directory as file destination")
		}
		entries, err := os.ReadDir(filepath.Join(dest, "source"))
		if err != nil || len(entries) != 0 {
			t.Fatal("copied into replacement directory", err)
		}
	}
}

func TestPacketError(t *testing.T) {
	r, w := io.Pipe()
	r.Close()
	s := &SFTP{in: w}
	if err := s.packet(fxWrite, []byte("data")); err == nil {
		t.Fatal("lost write failure")
	}
	w.Close()
}

type pacedReader struct{ remaining int }

func (r *pacedReader) Read(b []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	time.Sleep(5 * time.Millisecond)
	n := min(len(b), r.remaining)
	clear(b[:n])
	r.remaining -= n
	return n, nil
}
func TestUploadDeadlineTracksRepliesWhileFillingWindow(t *testing.T) {
	s := protocolFixture(t, func(typ byte, b []byte) (byte, []byte) {
		return fxStatus, fields(b[:4], u32(0), str(""), str(""))
	})
	s.timeout = 100 * time.Millisecond
	// Filling 64 requests takes >300 ms; individual replies keep arriving.
	const size = 100 * 32768
	if err := s.uploadData(&pacedReader{size}, "handle", size, nil); err != nil {
		t.Fatal(err)
	}
}
