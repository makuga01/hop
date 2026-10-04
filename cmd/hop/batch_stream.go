package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const batchDownloadPython = batchCompressionPython + `import os,sys,json,base64,stat,struct,hashlib,gzip
manifest=json.load(gzip.GzipFile(fileobj=sys.stdin.buffer,mode='rb'))
requests=manifest['files'];hashes=manifest['hashes']
out=batch_output()
def identity(a): return (a.st_dev,a.st_ino,a.st_size,a.st_mtime_ns,a.st_ctime_ns)
seen={}
for index,name,size,local_hash in requests:
 local_hash=hashes[local_hash-1] if local_hash else ''
 p=base64.b64decode(name,validate=True)
 fd=os.open(p,os.O_RDONLY|os.O_NONBLOCK)
 with os.fdopen(fd,'rb') as f:
  before=os.fstat(f.fileno())
  if not stat.S_ISREG(before.st_mode) or before.st_size!=size: raise RuntimeError('source changed at entry %d'%index)
  key=identity(before)+(before.st_mode,)
  if local_hash:
   if size<=1048576:
    data=f.read(size+1);length=len(data);digest=hashlib.sha256(data).digest()
   else:
    h=hashlib.sha256();length=0
    while length<=size:
     chunk=f.read(min(1048576,size+1-length))
     if not chunk: break
     h.update(chunk);length+=len(chunk)
    digest=h.digest()
   after=os.fstat(f.fileno())
   if length!=size or identity(before)!=identity(after) or identity(after)!=identity(os.stat(p)): raise RuntimeError('source changed at entry %d'%index)
   reusable=True
   if base64.b64decode(local_hash,validate=True)==digest:
    out.write(struct.pack('>IQII',index,size,before.st_mode|2147483648,index));out.write(digest)
   elif size>1048576:
    out.write(struct.pack('>IQI',index,size,before.st_mode|1073741824));reusable=False
   else:
    out.write(struct.pack('>IQI',index,size,before.st_mode));out.write(data);out.write(digest)
   if reusable and before.st_mode&256: seen[key]=(index,digest)
   if index%64==0: out.flush()
   continue
  if key in seen:
   original,digest=seen[key]
   if identity(before)!=identity(os.stat(p)): raise RuntimeError('source changed at entry %d'%index)
   out.write(struct.pack('>IQII',index,size,before.st_mode|2147483648,original));out.write(digest)
   if index%64==0: out.flush()
   continue
  out.write(struct.pack('>IQI',index,size,before.st_mode))
  digest=hashlib.sha256();remaining=size
  while remaining:
   data=f.read(min(1048576,remaining))
   if not data: raise RuntimeError('source truncated at entry %d'%index)
   out.write(data);digest.update(data);remaining-=len(data)
  after=os.fstat(f.fileno())
  if f.read(1) or identity(before)!=identity(after) or identity(after)!=identity(os.stat(p)): raise RuntimeError('source changed at entry %d'%index)
 if before.st_mode&256: seen[key]=(index,digest.digest())
 out.write(digest.digest())
 if index%64==0: out.flush()
out.write(struct.pack('>I',4294967295));out.close()
`

// Fresh downloads and authorized replacements share one bounded stream.
// Changed large replacements are deferred to native delta reuse.
func streamDownloads(s *SFTP, plan CopyPlan, o Options, p *batchProgress, completed []bool) error {
	if !plan.remoteReadChecked || len(plan.Files) < 32 || o.DryRun || os.Getenv("HOP_TRANSFER_BACKEND") == "sftp" {
		return nil
	}
	ctx, cancel := context.WithCancel(s.externalContext())
	defer cancel()
	indices := []int{}
	requests := [][4]any{}
	localHashes := map[int][32]byte{}
	hashIDs := map[[32]byte]int{}
	var hashes []string
	var candidates [][32]byte
	var known []bool
	if o.Overwrite {
		candidates = make([][32]byte, len(plan.Files))
		known = make([]bool, len(plan.Files))
		if err := parallelFilesN(len(plan.Files), 8, func(i int) error {
			f := plan.Files[i]
			if f.Replace {
				candidates[i], known[i] = streamBasisHash(ctx, f.Local, f.Size)
			}
			return ctx.Err()
		}); err != nil {
			return err
		}
	}
	for i, f := range plan.Files {
		if !f.Replace || o.Overwrite {
			hashID := 0
			if f.Replace && o.Overwrite {
				if digest, ok := candidates[i], known[i]; ok {
					localHashes[i] = digest
					hashID = hashIDs[digest]
					if hashID == 0 {
						hashes = append(hashes, base64.StdEncoding.EncodeToString(digest[:]))
						hashID = len(hashes)
						hashIDs[digest] = hashID
					}
				}
			}
			if f.Replace && f.Size > 1<<20 && hashID == 0 {
				continue // Unknown large bases keep the existing delta/SFTP path.
			}
			indices = append(indices, i)
			requests = append(requests, [4]any{i, base64.StdEncoding.EncodeToString([]byte(f.Remote)), f.Size, hashID})
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if len(indices) < 32 {
		return nil
	}
	var cmd *exec.Cmd
	if s.batchCommand != nil {
		cmd = s.batchCommand(ctx)
	} else {
		if s.cmd == nil || s.cmd.Path != "/usr/bin/ssh" || !validTarget(s.host.Target) {
			return nil
		}
		cmd = exec.CommandContext(ctx, "/usr/bin/ssh", append(rsyncSSHArgs(s.host), "--", s.host.Target, "python3 -c "+shellQuote(batchDownloadPython))...)
	}
	input, err := json.Marshal(struct {
		Files  [][4]any `json:"files"`
		Hashes []string `json:"hashes"`
	}{requests, hashes})
	if err != nil {
		return err
	}
	var packed bytes.Buffer
	compressor, _ := gzip.NewWriterLevel(&packed, gzip.BestSpeed)
	if _, err = compressor.Write(input); err != nil {
		return err
	}
	if err = compressor.Close(); err != nil {
		return err
	}
	cmd.Stdin = bytes.NewReader(packed.Bytes())
	diagnostics := &sshDiagnostics{}
	cmd.Stderr = diagnostics
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	if err = cmd.Start(); err != nil {
		return nil
	}
	timeout := s.timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	timer := time.AfterFunc(timeout, cancel)
	defer timer.Stop()
	reader := bufio.NewReader(output)
	magic := make([]byte, len("HOPFILES1\n"))
	if _, err = io.ReadFull(reader, magic); err != nil || (string(magic) != "HOPFILES1\n" && string(magic) != "HOPFILEZ1\n") {
		cancel()
		cmd.Wait()
		return nil
	}
	compressed, err := batchDecompressor(string(magic), reader)
	if err != nil {
		cancel()
		cmd.Wait()
		return err
	}
	defer compressed.Close()
	p.setBackend("Stream")
	type job struct {
		index    int
		mode     uint32
		data     []byte
		file     *os.File
		progress *fileProgress
		ready    chan struct{}
		original os.FileInfo
	}
	jobs := make(chan job, 16)
	ready := map[int]chan struct{}{}
	verified := map[int][32]byte{}
	deferred := map[int]bool{}
	var wg sync.WaitGroup
	var commitErr error
	flush := make(chan struct{}, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		process := func(batch []job) {
			pending := make([]*pendingStreamFile, len(batch))
			committed := make([]bool, len(batch))
			for i, j := range batch {
				f := plan.Files[j.index]
				pending[i] = &pendingStreamFile{to: f.Local, data: j.data, mode: j.mode, replace: f.Replace && o.Overwrite, file: j.file, original: j.original}
				if batch[i].progress == nil {
					batch[i].progress = p.startFile(f, true, plan.Destination)
				}
			}
			phase := func(action func(*pendingStreamFile) error) error {
				return parallelFilesN(len(batch), 8, func(i int) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					return action(pending[i])
				})
			}
			e := phase((*pendingStreamFile).prepare)
			for i := range pending {
				pending[i].data = nil
				batch[i].data = nil
			}
			if e == nil {
				e = syncStreamBatch(ctx, pending)
			}
			if e == nil {
				e = parallelFilesN(len(batch), 8, func(i int) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					if err := pending[i].publish(); err != nil {
						return err
					}
					committed[i] = true
					return nil
				})
			}
			for i, j := range batch {
				pending[i].cleanup()
				result := e
				if committed[i] {
					result = nil
					j.progress.update(plan.Files[j.index].Size)
					completed[j.index] = true
				}
				j.progress.complete(result)
				close(j.ready)
			}
			if e != nil {
				// Cancellation after a protocol error must not hide that error.
				if commitErr == nil && !errors.Is(e, context.Canceled) && !errors.Is(e, context.DeadlineExceeded) {
					commitErr = e
				}
				cancel()
			}
		}
		for first := range jobs {
			batch := []job{first}
			size := len(first.data)
			deadline := time.NewTimer(100 * time.Millisecond)
			closed := false
		gather:
			for len(batch) < 64 && size < 8<<20 {
				select {
				case j, ok := <-jobs:
					if !ok {
						closed = true
						break gather
					}
					batch = append(batch, j)
					size += len(j.data)
				case <-flush:
					break gather
				case <-deadline.C:
					break gather
				case <-ctx.Done():
					break gather
				}
			}
			deadline.Stop()
			process(batch)
			if closed {
				return
			}
		}
	}()
	for _, expected := range indices {
		var header [16]byte
		if _, err = io.ReadFull(compressed, header[:]); err != nil {
			break
		}
		index := binary.BigEndian.Uint32(header[:4])
		size := binary.BigEndian.Uint64(header[4:12])
		mode := binary.BigEndian.Uint32(header[12:])
		reference := mode&0x80000000 != 0
		deferLarge := mode&0x40000000 != 0
		mode &^= 0xc0000000
		if uint64(index) != uint64(expected) || size != plan.Files[expected].Size || mode&0170000 != 0100000 {
			err = errors.New("invalid batch file header")
			break
		}
		if deferLarge {
			if reference || !plan.Files[expected].Replace || !o.Overwrite || size <= 1<<20 {
				err = errors.New("invalid deferred batch file")
				break
			}
			deferred[expected] = true
			timer.Reset(timeout)
			continue
		}
		j := job{index: expected, mode: mode, ready: make(chan struct{})}
		ready[expected] = j.ready
		var digest [32]byte
		var basis *os.File
		var dataReader io.Reader = compressed
		if reference {
			var ref [4]byte
			if _, err = io.ReadFull(compressed, ref[:]); err != nil {
				break
			}
			index := int(binary.BigEndian.Uint32(ref[:]))
			previous, ok := verified[index]
			if index == expected {
				previous, ok = localHashes[index]
			}
			if !ok || index > expected || plan.Files[index].Size != size {
				err = errors.New("invalid batch reference")
				break
			}
			if _, err = io.ReadFull(compressed, digest[:]); err != nil {
				break
			}
			if previous != digest {
				err = errors.New("reference checksum mismatch")
				break
			}
			if index != expected {
				// A reference may need a file still in the current commit batch.
				// Flush it now rather than waiting for the batch to fill.
				select {
				case <-ready[index]:
				default:
					select {
					case flush <- struct{}{}:
					default:
					}
				}
				select {
				case <-ready[index]:
				case <-ctx.Done():
					err = ctx.Err()
				}
				if err != nil {
					break
				}
				if !completed[index] {
					err = errors.New("reference file was not committed")
					break
				}
			}

			basis, err = os.Open(plan.Files[index].Local)
			if err != nil {
				break
			}
			info, e := basis.Stat()
			if e != nil || !info.Mode().IsRegular() || uint64(info.Size()) != size {
				basis.Close()
				err = errors.New("local reference changed")
				break
			}
			dataReader = basis
			if index == expected && size > 1<<20 {
				j.original = info
			}
		}
		hash := sha256.New()
		if j.original != nil {
			// Recheck the complete local content, but do not stage another copy
			// of an unchanged large file. Publication revalidates its identity.
			var n int64
			n, err = io.Copy(hash, io.LimitReader(streamContextReader{ctx, dataReader}, int64(size)+1))
			if err == nil && n != int64(size) {
				err = errors.New("local reference changed")
			}
		} else if size <= 1<<20 {
			j.data = make([]byte, int(size))
			_, err = io.ReadFull(dataReader, j.data)
			hash.Write(j.data)
		} else {
			j.file, err = os.CreateTemp(filepath.Dir(plan.Files[expected].Local), ".hop-*.partial")
			if err == nil {
				j.progress = p.startFile(plan.Files[expected], true, plan.Destination)
				buffer := make([]byte, 256<<10)
				var received uint64
				for received < size && err == nil {
					var n int
					n, err = io.ReadFull(dataReader, buffer[:min(uint64(len(buffer)), size-received)])
					if err == nil {
						_, err = j.file.Write(buffer[:n])
						hash.Write(buffer[:n])
						received += uint64(n)
						j.progress.update(received)
						timer.Reset(timeout)
					}
				}
			}
		}
		if basis != nil {
			basis.Close()
		}
		cleanup := func() {
			if j.file != nil {
				j.file.Close()
				os.Remove(j.file.Name())
				if j.progress != nil {
					j.progress.complete(err)
				}
			}
		}
		if err != nil {
			cleanup()
			break
		}
		if !reference {
			if _, err = io.ReadFull(compressed, digest[:]); err != nil {
				cleanup()
				break
			}
		}
		if !bytes.Equal(hash.Sum(nil), digest[:]) {
			err = errors.New("batch checksum mismatch")
			cleanup()
			break
		}
		verified[expected] = digest
		timer.Reset(timeout)
		select {
		case jobs <- j:
		case <-ctx.Done():
			err = ctx.Err()
			cleanup()
		}

		if err != nil {
			break
		}
	}
	if err == nil {
		var end [4]byte
		_, err = io.ReadFull(compressed, end[:])
		if err == nil && binary.BigEndian.Uint32(end[:]) != 0xffffffff {
			err = errors.New("missing batch completion")
		}
		if err == nil {
			var extra [1]byte
			n, e := compressed.Read(extra[:])
			if n != 0 || e != io.EOF {
				err = errors.New("invalid batch trailer")
			}
		}
	}
	if err != nil {
		cancel()
	}
	close(jobs)
	wg.Wait()
	runErr := cmd.Wait()
	if commitErr != nil {
		return commitErr
	}
	if err != nil {
		return fmt.Errorf("batch download stopped: %w%s", err, diagnostics.suffix())
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if runErr != nil {
		return fmt.Errorf("batch download stopped: %w%s", runErr, diagnostics.suffix())
	}
	// A complete wire stream is not enough: cancellation can leave queued
	// commits unfinished. Never report success (or allow Move to remove sources)
	// until every selected destination has been committed.
	for _, index := range indices {
		if !completed[index] && !deferred[index] {
			return errors.New("batch download stopped before all files were committed")
		}
	}
	return nil
}

// A pending file is not published until the entire batch has finished syncing.
// Keeping writes, syncs and publication in separate phases avoids flushing
// newly-created metadata between every pair of small files.
type pendingStreamFile struct {
	to       string
	data     []byte
	mode     uint32
	replace  bool
	file     *os.File
	original os.FileInfo
	synced   bool
}

func (p *pendingStreamFile) prepare() error {
	if p.original != nil {
		return p.prepareUnchanged()
	}
	if p.file != nil {
		return p.file.Chmod(os.FileMode(p.mode & 0777))
	}

	to, data, mode, replace := p.to, p.data, p.mode, p.replace
	original, err := os.Lstat(to)
	p.original = original
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if exists {
		if !original.Mode().IsRegular() {
			return errors.New("destination is not a regular file")
		}
		if !replace {
			return &DestinationExistsError{Path: to}
		}
		// Exact comparison avoids rewriting identical files, without trusting dates.
		if original.Size() == int64(len(data)) {
			old, e := os.Open(to)
			if e != nil {
				return e
			}
			current, e := old.Stat()
			if e != nil || !os.SameFile(original, current) {
				old.Close()
				return errors.New("destination changed while opening")
			}
			contents, e := io.ReadAll(io.LimitReader(old, int64(len(data))+1))
			after, statErr := old.Stat()
			stable := statErr == nil && after.Size() == original.Size() && after.ModTime() == original.ModTime()
			if e == nil && stable && bytes.Equal(contents, data) {
				// Apply source permissions through the open handle, never through a symlink.
				if original.Mode().Perm() != os.FileMode(mode&0777) {
					e = old.Chmod(os.FileMode(mode & 0777))
					if e == nil {
						e = old.Sync()
					}
				}
				closeErr := old.Close()
				if e != nil {
					return e
				}
				if closeErr != nil {
					return closeErr
				}
				now, e := os.Lstat(to)
				if e != nil || !os.SameFile(original, now) || !now.Mode().IsRegular() || now.Size() != original.Size() || !now.ModTime().Equal(original.ModTime()) {
					return errors.New("destination changed during comparison")
				}
				return nil
			}
			old.Close()
			if e != nil {
				return e
			}
			if !stable {
				return errors.New("destination changed during comparison")
			}
		}
	}
	f, err := os.CreateTemp(filepath.Dir(to), ".hop-*.partial")
	if err != nil {
		return err
	}
	p.file = f
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(os.FileMode(mode & 0777)); err != nil {
		return err
	}
	return nil
}

// The receiver already verified this large file against the remote SHA-256.
// Keep its inode and timestamps, and apply permissions through a checked handle.
func (p *pendingStreamFile) prepareUnchanged() error {
	pathInfo, err := os.Lstat(p.to)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(p.original, pathInfo) {
		return errors.New("verified destination changed")
	}
	f, err := os.Open(p.to)
	if err != nil {
		return err
	}
	defer f.Close()
	now, err := f.Stat()
	if err != nil || !now.Mode().IsRegular() || !os.SameFile(p.original, now) || now.Size() != p.original.Size() || now.ModTime() != p.original.ModTime() {
		return errors.New("verified destination changed")
	}
	if now.Mode().Perm() != os.FileMode(p.mode&0777) {
		if err := f.Chmod(os.FileMode(p.mode & 0777)); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
	}
	return f.Close()
}

func (p *pendingStreamFile) sync() error {
	if p.file == nil {
		return nil
	}
	if err := p.file.Sync(); err != nil {
		return err
	}
	if err := p.file.Close(); err != nil {
		return err
	}
	p.synced = true
	return nil
}

func (p *pendingStreamFile) publish() error {
	if p.original != nil {
		now, err := os.Lstat(p.to)
		if err != nil || !now.Mode().IsRegular() || !os.SameFile(p.original, now) || now.Size() != p.original.Size() || now.ModTime() != p.original.ModTime() {
			return errors.New("destination changed before replacement")
		}
	}
	if p.file == nil {
		return nil
	}
	if !p.synced {
		return errors.New("cannot publish an unsynced stream file")
	}
	if p.original != nil && p.replace {
		return os.Rename(p.file.Name(), p.to)
	}
	return os.Link(p.file.Name(), p.to)
}

func (p *pendingStreamFile) cleanup() {
	if p.file != nil {
		p.file.Close()
		os.Remove(p.file.Name())
	}
}

func commitStreamFile(to string, data []byte, mode uint32, replace bool) error {
	p := &pendingStreamFile{to: to, data: data, mode: mode, replace: replace}
	defer p.cleanup()
	if err := p.prepare(); err != nil {
		return err
	}
	if err := p.sync(); err != nil {
		return err
	}
	return p.publish()
}

// Called only after the entire stream file and its checksum have been verified.
func commitStreamTemp(f *os.File, to string, mode uint32) error {
	p := &pendingStreamFile{file: f, to: to, mode: mode}
	defer p.cleanup()
	if err := p.prepare(); err != nil {
		return err
	}
	if err := p.sync(); err != nil {
		return err
	}
	return p.publish()
}

// Hash regular local files that the user authorized replacing, without buffering
// their contents in memory. This includes large files for the unchanged fast path.
// Revalidate the open handle to avoid trusting a changed path or stale metadata.
func streamBasisHash(ctx context.Context, name string, size uint64) ([32]byte, bool) {
	var empty [32]byte
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(size) {
		return empty, false
	}
	f, err := os.Open(name)
	if err != nil {
		return empty, false
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !os.SameFile(info, before) {
		return empty, false
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(streamContextReader{ctx, f}, int64(size)+1))
	after, e := f.Stat()
	if err != nil || e != nil || n != int64(size) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return empty, false
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, true
}

type streamContextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r streamContextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(b)
}
