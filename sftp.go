package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// SFTP v3 travels over the user's OpenSSH process. No shell command is used
// for file names, and no third-party SSH implementation handles credentials.
const (
	fxInit     = 1
	fxVersion  = 2
	fxOpen     = 3
	fxClose    = 4
	fxRead     = 5
	fxWrite    = 6
	fxLstat    = 7
	fxSetstat  = 9
	fxOpendir  = 11
	fxReaddir  = 12
	fxRemove   = 13
	fxMkdir    = 14
	fxRmdir    = 15
	fxRealpath = 16
	fxStat     = 17
	fxRename   = 18
	fxStatus   = 101
	fxHandle   = 102
	fxData     = 103
	fxName     = 104
	fxAttrs    = 105
	fxExtended = 200
	fxEOF      = 1
	fxNoSuch   = 2
)

type StatusError struct {
	Code    uint32
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("SFTP: %s (code %d)", safeText(e.Message), e.Code)
}
func noSuch(e error) bool   { var s *StatusError; return errors.As(e, &s) && s.Code == fxNoSuch }
func endOfDir(e error) bool { var s *StatusError; return errors.As(e, &s) && s.Code == fxEOF }

type Attr struct {
	Size  uint64
	Mode  uint32
	Mtime uint32
	Flags uint32
}

func (a Attr) Dir() bool     { return a.Mode&0170000 == 0040000 }
func (a Attr) Regular() bool { return a.Mode&0170000 == 0100000 }

type Entry struct {
	Name string
	Attr Attr
}
type SFTP struct {
	in        io.WriteCloser
	out       io.Reader
	cmd       *exec.Cmd
	id        uint32
	mu        sync.Mutex
	ext       map[string]string
	closeOnce sync.Once
	abortOnce sync.Once
	timeout   time.Duration
}

func sshArgs(h Host) []string {
	a := []string{"-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2"}
	return append(a, h.Options...)
}
func connectSFTP(h Host) (*SFTP, error) { return connectSFTPContext(context.Background(), h) }
func connectSFTPContext(ctx context.Context, h Host) (*SFTP, error) {
	args := append(sshArgs(h), "-T", "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "RemoteCommand=none", "-s", "--", h.Target, "sftp")
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	diagnostics := &sshDiagnostics{}
	cmd.Stderr = io.MultiWriter(os.Stderr, diagnostics)
	in, e := cmd.StdinPipe()
	if e != nil {
		return nil, e
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		in.Close()
		return nil, e
	}
	if e = cmd.Start(); e != nil {
		in.Close()
		return nil, e
	}
	s := &SFTP{in: in, out: out, cmd: cmd, ext: map[string]string{}, timeout: 45 * time.Second}
	// Authentication may need a password, key passphrase, or host-key prompt.
	timer := time.AfterFunc(2*time.Minute, s.Abort)
	defer timer.Stop()
	if e = s.packet(fxInit, u32(3)); e != nil {
		s.Close()
		return nil, e
	}
	t, b, e := s.receive()
	if e != nil {
		s.Close()
		return nil, fmt.Errorf("SFTP connection failed: %w%s", e, diagnostics.suffix())
	}
	d := decoder{b: b}
	version := d.u32()
	if t != fxVersion || version != 3 {
		s.Close()
		return nil, fmt.Errorf("server must support SFTP v3 (received %d)", version)
	}
	for d.remaining() > 0 && d.err == nil {
		k := d.str()
		v := d.str()
		s.ext[k] = v
	}
	if d.err != nil {
		s.Close()
		return nil, d.err
	}
	return s, nil
}
func (s *SFTP) Abort() {
	s.abortOnce.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
		s.in.Close()
	})
}
func (s *SFTP) Close() {
	s.closeOnce.Do(func() {
		s.in.Close()
		if s.cmd != nil {
			timer := time.AfterFunc(time.Second, s.Abort)
			_ = s.cmd.Wait()
			timer.Stop()
		}
	})
}
func u32(n uint32) []byte           { b := make([]byte, 4); binary.BigEndian.PutUint32(b, n); return b }
func u64(n uint64) []byte           { b := make([]byte, 8); binary.BigEndian.PutUint64(b, n); return b }
func str(s string) []byte           { return append(u32(uint32(len(s))), []byte(s)...) }
func fields(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
func (s *SFTP) packet(t byte, p []byte) error {
	b := fields(u32(uint32(len(p)+1)), []byte{t}, p)
	for len(b) > 0 {
		n, e := s.in.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func (s *SFTP) receive() (byte, []byte, error) {
	var hdr [4]byte
	if _, e := io.ReadFull(s.out, hdr[:]); e != nil {
		return 0, nil, e
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n < 1 || n > 4<<20 {
		return 0, nil, fmt.Errorf("invalid SFTP packet size: %d", n)
	}
	b := make([]byte, n)
	_, e := io.ReadFull(s.out, b)
	return b[0], b[1:], e
}
func (s *SFTP) send(t byte, p []byte) (uint32, error) {
	s.id++
	return s.id, s.packet(t, fields(u32(s.id), p))
}
func (s *SFTP) reply(id uint32) (byte, []byte, error) {
	got, t, b, e := s.replyAny()
	if got != id && e == nil {
		return 0, nil, errors.New("unexpected SFTP response id")
	}
	return t, b, e
}
func (s *SFTP) replyAny() (uint32, byte, []byte, error) {
	t, b, e := s.receive()
	if e != nil {
		return 0, 0, nil, e
	}
	if len(b) < 4 {
		return 0, 0, nil, errors.New("missing SFTP response id")
	}
	id := binary.BigEndian.Uint32(b)
	b = b[4:]
	if t == fxStatus {
		d := decoder{b: b}
		code := d.u32()
		msg := d.str()
		if d.err != nil {
			return id, 0, nil, d.err
		}
		if code != 0 {
			return id, t, nil, &StatusError{code, msg}
		}
	}
	return id, t, b, nil
}
func (s *SFTP) request(t byte, p []byte, want byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	timer := time.AfterFunc(s.timeout, s.Abort)
	defer timer.Stop()
	id, e := s.send(t, p)
	if e != nil {
		return nil, e
	}
	got, b, e := s.reply(id)
	if e != nil {
		return nil, e
	}
	if got != want {
		return nil, fmt.Errorf("unexpected SFTP packet %d (wanted %d)", got, want)
	}
	return b, nil
}

type decoder struct {
	b   []byte
	at  int
	err error
}

func (d *decoder) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || n > len(d.b)-d.at {
		d.err = io.ErrUnexpectedEOF
		return nil
	}
	v := d.b[d.at : d.at+n]
	d.at += n
	return v
}
func (d *decoder) u32() uint32 {
	b := d.take(4)
	if len(b) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}
func (d *decoder) u64() uint64 {
	b := d.take(8)
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}
func (d *decoder) str() string    { n := d.u32(); return string(d.take(int(n))) }
func (d *decoder) remaining() int { return len(d.b) - d.at }
func (d *decoder) attr() Attr {
	a := Attr{Flags: d.u32()}
	if a.Flags&1 != 0 {
		a.Size = d.u64()
	}
	if a.Flags&2 != 0 {
		d.take(8)
	}
	if a.Flags&4 != 0 {
		a.Mode = d.u32()
	}
	if a.Flags&8 != 0 {
		d.u32()
		a.Mtime = d.u32()
	}
	if a.Flags&0x80000000 != 0 {
		n := d.u32()
		if n > 10000 {
			d.err = errors.New("too many SFTP attributes")
			return a
		}
		for i := uint32(0); i < n && d.err == nil; i++ {
			d.str()
			d.str()
		}
	}
	return a
}
func (s *SFTP) Realpath(p string) (string, error) {
	b, e := s.request(fxRealpath, str(p), fxName)
	if e != nil {
		return "", e
	}
	d := decoder{b: b}
	if d.u32() < 1 {
		return "", errors.New("empty realpath result")
	}
	name := d.str()
	return name, d.err
}
func (s *SFTP) Stat(p string, follow bool) (Attr, error) {
	t := byte(fxLstat)
	if follow {
		t = fxStat
	}
	b, e := s.request(t, str(p), fxAttrs)
	if e != nil {
		return Attr{}, e
	}
	d := decoder{b: b}
	a := d.attr()
	return a, d.err
}
func (s *SFTP) open(p string, flags uint32, mode uint32) (string, error) {
	attrs := u32(0)
	if mode != 0 {
		attrs = fields(u32(4), u32(mode))
	}
	b, e := s.request(fxOpen, fields(str(p), u32(flags), attrs), fxHandle)
	if e != nil {
		return "", e
	}
	d := decoder{b: b}
	h := d.str()
	return h, d.err
}
func (s *SFTP) closeHandle(h string) error { _, e := s.request(fxClose, str(h), fxStatus); return e }
func (s *SFTP) ReadDir(p string) ([]Entry, error) {
	b, e := s.request(fxOpendir, str(p), fxHandle)
	if e != nil {
		return nil, e
	}
	d := decoder{b: b}
	h := d.str()
	if d.err != nil {
		return nil, d.err
	}
	defer s.closeHandle(h)
	var entries []Entry
	for len(entries) < 10000 {
		b, e = s.request(fxReaddir, str(h), fxName)
		if endOfDir(e) {
			return entries, nil
		}
		if e != nil {
			return entries, e
		}
		d = decoder{b: b}
		n := d.u32()
		if n > 10000 {
			return nil, errors.New("directory response too large")
		}
		if n == 0 {
			return entries, nil
		}
		for i := uint32(0); i < n && d.err == nil; i++ {
			name := d.str()
			d.str()
			a := d.attr()
			if name != "." && name != ".." && !bytes.ContainsAny([]byte(name), "/\x00") {
				entries = append(entries, Entry{name, a})
			}
		}
		if d.err != nil {
			return nil, d.err
		}
	}
	return entries, errors.New("directory exceeds 10,000 entries; narrow the path")
}
func (s *SFTP) Tail(p string, max uint64) (string, error) {
	a, e := s.Stat(p, true)
	if e != nil {
		return "", e
	}
	if !a.Regular() {
		return "", errors.New("history is not a regular file")
	}
	h, e := s.open(p, 1, 0)
	if e != nil {
		return "", e
	}
	defer s.closeHandle(h)
	start := uint64(0)
	if a.Size > max {
		start = a.Size - max
	}
	var b bytes.Buffer
	for off := start; off < a.Size; {
		n := uint32(32768)
		if a.Size-off < uint64(n) {
			n = uint32(a.Size - off)
		}
		r, e := s.request(fxRead, fields(str(h), u64(off), u32(n)), fxData)
		if endOfDir(e) {
			break
		}
		if e != nil {
			return "", e
		}
		d := decoder{b: r}
		chunk := d.str()
		if d.err != nil {
			return "", d.err
		}
		if len(chunk) == 0 {
			return "", io.ErrNoProgress
		}
		b.WriteString(chunk)
		off += uint64(len(chunk))
	}
	result := b.String()
	if start > 0 {
		if i := bytes.IndexByte([]byte(result), '\n'); i >= 0 {
			result = result[i+1:]
		}
	}
	return result, nil
}
func (s *SFTP) Remove(p string) error { _, e := s.request(fxRemove, str(p), fxStatus); return e }
func (s *SFTP) Mkdir(p string, mode uint32) error {
	_, e := s.request(fxMkdir, fields(str(p), u32(4), u32(mode&0777)), fxStatus)
	return e
}
func (s *SFTP) Chmod(p string, mode uint32) error {
	_, e := s.request(fxSetstat, fields(str(p), u32(4), u32(mode&0777)), fxStatus)
	return e
}
func (s *SFTP) Rename(from, to string, overwrite bool) error {
	if overwrite {
		if s.ext["posix-rename@openssh.com"] != "1" {
			return errors.New("server lacks atomic overwrite support; choose a new file name")
		}
		_, e := s.request(fxExtended, fields(str("posix-rename@openssh.com"), str(from), str(to)), fxStatus)
		return e
	}
	_, e := s.request(fxRename, fields(str(from), str(to)), fxStatus)
	return e
}

// Pipeline requests so each 32 KiB chunk does not cost an SSH round trip.
func (s *SFTP) Upload(f *os.File, p string, size uint64, mode uint32, progress func(uint64)) error {
	h, e := s.open(p, 2|8|32, mode)
	if e != nil {
		return e
	} // WRITE|CREAT|EXCL
	e = s.uploadData(f, h, size, progress)
	ce := s.closeHandle(h)
	if e != nil {
		return e
	}
	return ce
}
func (s *SFTP) uploadData(f io.Reader, h string, size uint64, progress func(uint64)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var off uint64
	for off < size {
		timer := time.AfterFunc(s.timeout, s.Abort)
		var ids []uint32
		for n := 0; n < 16 && off < size; n++ {
			length := uint64(32768)
			if size-off < length {
				length = size - off
			}
			b := make([]byte, length)
			if _, e := io.ReadFull(f, b); e != nil {
				timer.Stop()
				s.Abort()
				return e
			}
			id, e := s.send(fxWrite, fields(str(h), u64(off), str(string(b))))
			if e != nil {
				timer.Stop()
				return e
			}
			ids = append(ids, id)
			off += length
		}
		var first error
		waiting := map[uint32]bool{}
		for _, id := range ids {
			waiting[id] = true
		}
		for range ids {
			id, t, _, e := s.replyAny()
			if !waiting[id] {
				timer.Stop()
				s.Abort()
				return errors.New("unexpected pipelined write response")
			}
			delete(waiting, id)
			if e == nil && t != fxStatus {
				e = errors.New("invalid write response")
			}
			if e != nil && first == nil {
				first = e
			}
		}
		timer.Stop()
		if first != nil {
			return first
		}
		progress(off)
	}
	return nil
}
func (s *SFTP) Download(p string, f *os.File, size uint64, progress func(uint64)) error {
	h, e := s.open(p, 1, 0)
	if e != nil {
		return e
	}
	e = s.downloadData(h, f, size, progress)
	ce := s.closeHandle(h)
	if e != nil {
		return e
	}
	return ce
}
func (s *SFTP) downloadData(h string, f *os.File, size uint64, progress func(uint64)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var off, total uint64
	type pending struct {
		id     uint32
		off    uint64
		length uint32
	}
	for off < size {
		timer := time.AfterFunc(s.timeout, s.Abort)
		var requests []pending
		for n := 0; n < 16 && off < size; n++ {
			length := uint32(32768)
			if size-off < uint64(length) {
				length = uint32(size - off)
			}
			id, e := s.send(fxRead, fields(str(h), u64(off), u32(length)))
			if e != nil {
				timer.Stop()
				return e
			}
			requests = append(requests, pending{id, off, length})
			off += uint64(length)
		}
		var first error
		var short []pending
		waiting := map[uint32]pending{}
		for _, r := range requests {
			waiting[r.id] = r
		}
		for range requests {
			id, t, b, e := s.replyAny()
			r, ok := waiting[id]
			if !ok {
				timer.Stop()
				s.Abort()
				return errors.New("unexpected pipelined read response")
			}
			delete(waiting, id)
			if e != nil {
				if first == nil {
					first = e
				}
				continue
			}
			if t != fxData {
				if first == nil {
					first = errors.New("invalid read response")
				}
				continue
			}
			d := decoder{b: b}
			chunk := d.str()
			if d.err != nil || len(chunk) == 0 || len(chunk) > int(r.length) {
				if first == nil {
					first = errors.New("invalid read length; source may have changed")
				}
				continue
			}
			_, e = f.WriteAt([]byte(chunk), int64(r.off))
			if e != nil && first == nil {
				first = e
			}
			total += uint64(len(chunk))
			if len(chunk) < int(r.length) {
				short = append(short, pending{off: r.off + uint64(len(chunk)), length: r.length - uint32(len(chunk))})
			}
		}
		for _, r := range short {
			for r.length > 0 && first == nil {
				id, e := s.send(fxRead, fields(str(h), u64(r.off), u32(r.length)))
				if e != nil {
					first = e
					break
				}
				t, b, e := s.reply(id)
				if e != nil {
					first = e
					break
				}
				if t != fxData {
					first = errors.New("invalid short-read reply")
					break
				}
				d := decoder{b: b}
				chunk := d.str()
				if d.err != nil || len(chunk) == 0 || len(chunk) > int(r.length) {
					first = errors.New("source changed during download")
					break
				}
				if _, e = f.WriteAt([]byte(chunk), int64(r.off)); e != nil {
					first = e
					break
				}
				r.off += uint64(len(chunk))
				r.length -= uint32(len(chunk))
				total += uint64(len(chunk))
			}
		}
		timer.Stop()
		if first != nil {
			return first
		}
		progress(total)
	}
	return nil
}

// Retain bounded connection diagnostics when leaving the alternate screen on error.
type sshDiagnostics struct{ data []byte }

func (d *sshDiagnostics) Write(p []byte) (int, error) {
	const limit = 16384
	n := len(p)
	if len(p) >= limit {
		d.data = append(d.data[:0], p[len(p)-limit:]...)
	} else {
		if len(d.data)+len(p) > limit {
			d.data = append(d.data[:0], d.data[len(d.data)+len(p)-limit:]...)
		}
		d.data = append(d.data, p...)
	}
	return n, nil
}
func (d *sshDiagnostics) suffix() string {
	msg := strings.TrimSpace(string(d.data))
	if msg == "" {
		return ""
	}
	return ": " + safeText(msg)
}

func (s *SFTP) Rmdir(p string) error { _, e := s.request(fxRmdir, str(p), fxStatus); return e }
