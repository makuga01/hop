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

const batchDownloadPython = `import os,sys,json,base64,stat,struct,hashlib,gzip
requests=json.load(gzip.GzipFile(fileobj=sys.stdin.buffer,mode='rb'))
sys.stdout.buffer.write(b'HOPFILES1\n');sys.stdout.buffer.flush()
out=gzip.GzipFile(fileobj=sys.stdout.buffer,mode='wb',compresslevel=1)
def identity(a): return (a.st_dev,a.st_ino,a.st_size,a.st_mtime_ns,a.st_ctime_ns)
seen={}
for index,name,size,local_hash in requests:
 p=base64.b64decode(name,validate=True)
 fd=os.open(p,os.O_RDONLY|os.O_NONBLOCK)
 with os.fdopen(fd,'rb') as f:
  before=os.fstat(f.fileno())
  if not stat.S_ISREG(before.st_mode) or before.st_size!=size: raise RuntimeError('source changed at entry %d'%index)
  key=identity(before)+(before.st_mode,)
  if local_hash:
   data=f.read(size+1);digest=hashlib.sha256(data).digest()
   after=os.fstat(f.fileno())
   if len(data)!=size or identity(before)!=identity(after) or identity(after)!=identity(os.stat(p)): raise RuntimeError('source changed at entry %d'%index)
   if base64.b64decode(local_hash,validate=True)==digest:
    out.write(struct.pack('>IQII',index,size,before.st_mode|2147483648,index));out.write(digest)
   else:
    out.write(struct.pack('>IQI',index,size,before.st_mode));out.write(data);out.write(digest)
   if before.st_mode&256: seen[key]=(index,digest)
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

// Fresh downloads share one bounded stream; authorized small replacements also
// use it. Existing large files retain native delta reuse.
func streamDownloads(s *SFTP, plan CopyPlan, o Options, p *batchProgress, completed []bool) error {
	if !plan.remoteReadChecked || o.DryRun || os.Getenv("HOP_TRANSFER_BACKEND") == "sftp" {
		return nil
	}
	indices := []int{}
	requests := [][4]any{}
	localHashes := map[int][32]byte{}
	for i, f := range plan.Files {
		if !f.Replace || (o.Overwrite && f.Size <= 1<<20) {
			indices = append(indices, i)
			hashText := ""
			if f.Replace && o.Overwrite && f.Size <= 1<<20 {
				if digest, ok := streamBasisHash(f.Local, f.Size); ok {
					localHashes[i] = digest
					hashText = base64.StdEncoding.EncodeToString(digest[:])
				}
			}
			requests = append(requests, [4]any{i, base64.StdEncoding.EncodeToString([]byte(f.Remote)), f.Size, hashText})
		}
	}
	if len(indices) < 32 {
		return nil
	}
	ctx, cancel := context.WithCancel(s.externalContext())
	defer cancel()
	var cmd *exec.Cmd
	if s.batchCommand != nil {
		cmd = s.batchCommand(ctx)
	} else {
		if s.cmd == nil || s.cmd.Path != "/usr/bin/ssh" || !validTarget(s.host.Target) {
			return nil
		}
		cmd = exec.CommandContext(ctx, "/usr/bin/ssh", append(rsyncSSHArgs(s.host), "--", s.host.Target, "python3 -c "+shellQuote(batchDownloadPython))...)
	}
	input, err := json.Marshal(requests)
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
	if _, err = io.ReadFull(reader, magic); err != nil || string(magic) != "HOPFILES1\n" {
		cancel()
		cmd.Wait()
		return nil
	}
	compressed, err := gzip.NewReader(reader)
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
	}
	jobs := make(chan job, 16)
	ready := map[int]chan struct{}{}
	verified := map[int][32]byte{}
	var wg sync.WaitGroup
	var once sync.Once
	var commitErr error
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					if j.file != nil {
						j.file.Close()
						os.Remove(j.file.Name())
						j.progress.complete(ctx.Err())
					}
					close(j.ready)
					continue
				}
				f := plan.Files[j.index]
				progress := j.progress
				if progress == nil {
					progress = p.startFile(f, true, plan.Destination)
				}
				var e error
				if j.file != nil {
					e = commitStreamTemp(j.file, f.Local, j.mode)
				} else {
					e = commitStreamFile(f.Local, j.data, j.mode, f.Replace && o.Overwrite)
				}
				if e == nil {
					progress.update(f.Size)
					completed[j.index] = true
				}
				progress.complete(e)
				close(j.ready)
				if e != nil {
					once.Do(func() { commitErr = e; cancel() })
				}
			}
		}()
	}
	for _, expected := range indices {
		var header [16]byte
		if _, err = io.ReadFull(compressed, header[:]); err != nil {
			break
		}
		index := binary.BigEndian.Uint32(header[:4])
		size := binary.BigEndian.Uint64(header[4:12])
		mode := binary.BigEndian.Uint32(header[12:])
		reference := mode&0x80000000 != 0
		mode &^= 0x80000000
		if uint64(index) != uint64(expected) || size != plan.Files[expected].Size || mode&0170000 != 0100000 {
			err = errors.New("invalid batch file header")
			break
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
		}
		hash := sha256.New()
		if size <= 1<<20 {
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
		if !completed[index] {
			return errors.New("batch download stopped before all files were committed")
		}
	}
	return nil
}

func commitStreamFile(to string, data []byte, mode uint32, replace bool) error {
	original, err := os.Lstat(to)
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
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(os.FileMode(mode & 0777)); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if exists && replace {
		now, e := os.Lstat(to)
		if e != nil || !now.Mode().IsRegular() || !os.SameFile(original, now) || now.Size() != original.Size() || now.ModTime() != original.ModTime() {
			return errors.New("destination changed before replacement")
		}
		return os.Rename(f.Name(), to)
	}
	return os.Link(f.Name(), to)
}

// Called only after the entire stream file and its checksum have been verified.
func commitStreamTemp(f *os.File, to string, mode uint32) error {
	defer f.Close()
	defer os.Remove(f.Name())
	if err := f.Chmod(os.FileMode(mode & 0777)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Link(f.Name(), to)
}

// Hash only small regular local files that the user authorized replacing.
// Revalidate the open handle to avoid trusting a changed path or stale metadata.
func streamBasisHash(name string, size uint64) ([32]byte, bool) {
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
	n, err := io.Copy(hash, io.LimitReader(f, int64(size)+1))
	after, e := f.Stat()
	if err != nil || e != nil || n != int64(size) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return empty, false
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest, true
}
