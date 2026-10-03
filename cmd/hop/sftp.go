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
	"sync/atomic"
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
	batchCommand   func(context.Context) *exec.Cmd
	host           Host
	scanCommand    func(context.Context) *exec.Cmd
	deltaHash      func(context.Context, string, uint64, uint64) ([]byte, error)
	deltaTransfers atomic.Uint64
	deltaReused    atomic.Uint64
	externalOnce   sync.Once
	externalCtx    context.Context
	externalCancel context.CancelFunc
	rsyncOnce      sync.Once
	rsyncCap       *rsyncCapability
	limitsOnce     sync.Once
	limits         transferLimits
	limitsErr      error
	in             io.WriteCloser
	out            io.Reader
	cmd            *exec.Cmd
	id             uint32
	mu             sync.Mutex
	writeMu        sync.Mutex
	readerOnce     sync.Once
	pending        map[uint32]sftpPending
	failure        error
	ext            map[string]string
	closeOnce      sync.Once
	abortOnce      sync.Once
	timeout        time.Duration
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
	s := &SFTP{host: h, in: in, out: out, cmd: cmd, ext: map[string]string{}, timeout: 45 * time.Second}
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
		s.stopExternal()
		s.fail(io.ErrClosedPipe)
		if s.cmd != nil && s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
		s.in.Close()
	})
}
func (s *SFTP) Close() {
	s.closeOnce.Do(func() {
		s.stopExternal()
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

// Reuse the encoded packet: one payload copy and one write per request.
var packetBuffers = sync.Pool{New: func() any { b := make([]byte, 256*1024+1024); return &b }}

func (s *SFTP) packet(t byte, parts ...[]byte) error {
	n := 5
	for _, p := range parts {
		n += len(p)
	}
	pooled := packetBuffers.Get().(*[]byte)
	defer packetBuffers.Put(pooled)
	b := *pooled
	if n > len(b) {
		b = make([]byte, n)
	} else {
		b = b[:n]
	}
	binary.BigEndian.PutUint32(b, uint32(n-4))
	b[4] = t
	at := 5
	for _, p := range parts {
		at += copy(b[at:], p)
	}
	for len(b) > 0 {
		n, err := s.in.Write(b)
		if err != nil {
			return err
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

// A single reader routes replies by ID while callers share the SSH connection.
// Buffered reply channels let it drain the SSH pipe even while a sender blocks.
type sftpPending struct {
	reply   chan sftpReply
	onReply func()
}

type sftpReply struct {
	typ  byte
	data []byte
	err  error
}

func (s *SFTP) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return
	}
	s.failure = err
	for id, ch := range s.pending {
		ch.reply <- sftpReply{err: err}
		delete(s.pending, id)
	}
}
func (s *SFTP) readReplies() {
	for {
		id, typ, data, err := s.replyAny()
		var status *StatusError
		if err != nil && !errors.As(err, &status) {
			s.fail(err)
			s.Abort()
			return
		}
		s.mu.Lock()
		ch, ok := s.pending[id]
		delete(s.pending, id)
		if ok {
			if ch.onReply != nil {
				ch.onReply()
			}
			ch.reply <- sftpReply{typ, data, err}
		}
		s.mu.Unlock()
		if !ok {
			s.fail(errors.New("unexpected SFTP response id"))
			s.Abort()
			return
		}
	}
}
func (s *SFTP) startRequest(t byte, parts ...[]byte) <-chan sftpReply {
	return s.startActiveRequest(t, nil, parts...)
}

// Transfer deadlines track replies even while the sender is filling its window.
// Otherwise a healthy slow upload could time out before filling 8 MiB.
func (s *SFTP) startActiveRequest(t byte, onReply func(), parts ...[]byte) <-chan sftpReply {
	s.readerOnce.Do(func() {
		s.mu.Lock()
		s.pending = make(map[uint32]sftpPending)
		s.mu.Unlock()
		go s.readReplies()
	})
	ch := make(chan sftpReply, 1)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	if s.failure != nil {
		ch <- sftpReply{err: s.failure}
		s.mu.Unlock()
		return ch
	}
	s.id++
	id := s.id
	s.pending[id] = sftpPending{ch, onReply}
	s.mu.Unlock()
	if err := s.packet(t, append([][]byte{u32(id)}, parts...)...); err != nil {
		s.fail(err)
		s.Abort()
	}
	return ch
}
func (s *SFTP) request(t byte, p []byte, want byte) ([]byte, error) {
	timer := time.AfterFunc(s.timeout, s.Abort)
	defer timer.Stop()
	r := <-s.startRequest(t, p)
	if r.err != nil {
		return nil, r.err
	}
	if r.typ != want {
		return nil, fmt.Errorf("unexpected SFTP packet %d (wanted %d)", r.typ, want)
	}
	return r.data, nil
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

const transferWindow = 64
const transferChunk = 32768

// Fill a bounded rolling window using the server-negotiated packet size.
func (s *SFTP) uploadData(f io.Reader, h string, size uint64, progress func(uint64)) error {
	timer := time.AfterFunc(s.timeout, s.Abort)
	defer timer.Stop()
	type write struct {
		reply  <-chan sftpReply
		length uint64
	}
	chunk, window, err := s.transferGeometry(h, true)
	if err != nil {
		return err
	}
	pending := make([]write, 0, window)
	var off, completed uint64
	var first error
	buf := make([]byte, min(uint64(chunk), size))
	for off < size || len(pending) > 0 {
		for first == nil && off < size && len(pending) < window {
			n := min(uint64(len(buf)), size-off)
			if _, err := io.ReadFull(f, buf[:n]); err != nil {
				first = err
				break
			}
			reply := s.startActiveRequest(fxWrite, func() { timer.Reset(s.timeout) }, str(h), u64(off), u32(uint32(n)), buf[:n])
			pending = append(pending, write{reply, n})
			off += n
		}
		if len(pending) == 0 {
			break
		}
		w := pending[0]
		pending = pending[1:]
		r := <-w.reply
		if r.err == nil && r.typ != fxStatus {
			r.err = errors.New("invalid write response")
		}
		if r.err != nil && first == nil {
			first = r.err
		}
		if r.err == nil {
			completed += w.length
			if progress != nil {
				progress(completed)
			}
		}
		timer.Reset(s.timeout)
	}
	return first
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
	timer := time.AfterFunc(s.timeout, s.Abort)
	defer timer.Stop()
	type read struct {
		reply  <-chan sftpReply
		off    uint64
		length uint32
	}
	chunk, window, err := s.transferGeometry(h, false)
	if err != nil {
		return err
	}
	pending := make([]read, 0, window)
	queue := func(off uint64, n uint32) {
		ch := s.startActiveRequest(fxRead, func() { timer.Reset(s.timeout) }, fields(str(h), u64(off), u32(n)))
		pending = append(pending, read{ch, off, n})
	}
	var off, total uint64
	var first error
	for off < size || len(pending) > 0 {
		for first == nil && off < size && len(pending) < window {
			n := uint32(min(uint64(chunk), size-off))
			queue(off, n)
			off += uint64(n)
		}
		if len(pending) == 0 {
			break
		}
		req := pending[0]
		pending = pending[1:]
		r := <-req.reply
		if r.err == nil && r.typ != fxData {
			r.err = errors.New("invalid read response")
		}
		if r.err != nil {
			if first == nil {
				first = r.err
			}
			continue
		}
		d := decoder{b: r.data}
		chunk := d.take(int(d.u32()))
		if d.err != nil || len(chunk) == 0 || len(chunk) > int(req.length) {
			if first == nil {
				first = errors.New("invalid read length; source may have changed")
			}
			continue
		}
		if first == nil {
			n, err := f.WriteAt(chunk, int64(req.off))
			if err == nil && n != len(chunk) {
				err = io.ErrShortWrite
			}
			if err != nil {
				first = err
			} else {
				total += uint64(n)
				if progress != nil {
					progress(total)
				}
				// Short reads are legal: refill their remainder through the same pipeline.
				if n < int(req.length) {
					queue(req.off+uint64(n), req.length-uint32(n))
				}
			}
		}
		timer.Reset(s.timeout)
	}
	return first
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
