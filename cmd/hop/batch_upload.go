package main

import (
	"bufio"
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
	"time"
)

// The helper only writes explicit destinations. Directory descriptors anchor
// temporary files and commits, so a renamed parent cannot redirect publication.
// Each file is verified before a bounded group is synced and acknowledged.
const batchUploadPython = `import os,sys,stat,struct,json,base64,gzip,hashlib,secrets,concurrent.futures
for function in (os.open,os.stat,os.unlink,os.link,os.rename):
 if function not in os.supports_dir_fd: raise RuntimeError('directory descriptors unavailable')
if os.stat not in os.supports_follow_symlinks: raise RuntimeError('nofollow stat unavailable')
sys.stdout.buffer.write(b'HOPUPG1\n');sys.stdout.buffer.flush()
stream=gzip.GzipFile(fileobj=sys.stdin.buffer.raw,mode='rb')
def read(n):
 b=stream.read(n)
 if len(b)!=n: raise RuntimeError('truncated upload')
 return b
def identity(a): return (a.st_dev,a.st_ino,a.st_size,a.st_mtime_ns,a.st_ctime_ns)
def lookup(fd,name):
 try: return os.stat(name,dir_fd=fd,follow_symlinks=False)
 except FileNotFoundError: return None
pending=[]
pool=concurrent.futures.ThreadPoolExecutor(max_workers=8)
def clean(j):
 if j['file'] is not None: j['file'].close();j['file']=None
 if j['temp'] is not None:
  try: os.unlink(j['temp'],dir_fd=j['parent'])
  except FileNotFoundError: pass
  j['temp']=None
 os.close(j['parent'])
def verify_parent(j):
 if identity(os.stat(j['directory']))[:2]!=j['parent_identity']: raise RuntimeError('upload parent changed')
def sync(j):
 j['file'].flush();os.fsync(j['file'].fileno());j['file'].close();j['file']=None
 verify_parent(j)
def commit():
 if not pending: return
 futures=[pool.submit(sync,j) for j in pending]
 concurrent.futures.wait(futures)
 for future in futures: future.result()
 for j in pending:
  verify_parent(j)
  current=lookup(j['parent'],j['name'])
  if j['original'] is not None:
   if current is None or not stat.S_ISREG(current.st_mode) or identity(current)!=identity(j['original']): raise RuntimeError('upload destination changed')
   os.replace(j['temp'],j['name'],src_dir_fd=j['parent'],dst_dir_fd=j['parent'])
  else:
   os.link(j['temp'],j['name'],src_dir_fd=j['parent'],dst_dir_fd=j['parent'],follow_symlinks=False)
  verify_parent(j)
  sys.stdout.buffer.write(struct.pack('>I',j['index']));sys.stdout.buffer.flush()
 for j in pending: clean(j)
 pending.clear()
try:
 length=struct.unpack('>I',read(4))[0]
 if length>33554432: raise RuntimeError('upload manifest too large')
 manifest=json.loads(read(length))
 root,canonical=[base64.b64decode(p,validate=True) for p in manifest["root"]]
 if os.path.realpath(root)!=canonical: raise RuntimeError("shell and SFTP destinations differ")
 requests=manifest["files"]
 if len(requests)>100000: raise RuntimeError('too many upload files')
 volume=0
 for expected,name,size,replace in requests:
  index,received,mode=struct.unpack('>IQI',read(16))
  if index!=expected or received!=size or mode&~511: raise RuntimeError('invalid upload header')
  destination=base64.b64decode(name,validate=True)
  directory,basename=os.path.split(destination)
  if not os.path.isabs(destination) or basename in (b'',b'.',b'..'): raise RuntimeError('invalid upload destination')
  parent=os.open(directory,os.O_RDONLY|os.O_DIRECTORY)
  j={'index':index,'directory':directory,'parent':parent,'parent_identity':identity(os.fstat(parent))[:2],'name':basename,'original':None,'temp':None,'file':None}
  pending.append(j)
  original=lookup(parent,basename);j['original']=original
  if original is not None and (not replace or not stat.S_ISREG(original.st_mode)): raise RuntimeError('upload destination exists or is not regular')
  temp=('.hop-'+secrets.token_hex(12)+'.partial').encode()
  fd=os.open(temp,os.O_WRONLY|os.O_CREAT|os.O_EXCL,384,dir_fd=parent)
  j['temp']=temp;j['file']=os.fdopen(fd,'wb')
  digest=hashlib.sha256();remaining=size
  while remaining:
   data=read(min(262144,remaining));j['file'].write(data);digest.update(data);remaining-=len(data)
  if read(32)!=digest.digest(): raise RuntimeError('upload checksum mismatch')
  os.fchmod(j['file'].fileno(),mode)
  volume+=size
  if len(pending)>=32 or volume>=8388608: commit();volume=0
 if read(4)!=b'\xff\xff\xff\xff' or stream.read(1): raise RuntimeError('invalid upload trailer')
 commit()
 sys.stdout.buffer.write(b'\xff\xff\xff\xff');sys.stdout.buffer.flush()
finally:
 pool.shutdown(wait=True)
 for j in pending:
  try: clean(j)
  except OSError: pass
`

func streamUploads(s *SFTP, plan CopyPlan, o Options, p *batchProgress, completed []bool) error {
	if o.DryRun || os.Getenv("HOP_TRANSFER_BACKEND") == "sftp" {
		return nil
	}
	indices := []int{}
	largeFresh := false
	requests := [][4]any{}
	for i, f := range plan.Files {
		// Keep existing large-file delta reuse; aggregate small replacements and
		// fresh files instead of performing multiple SFTP round trips per file.
		if f.Replace && (!o.Overwrite || f.Size > 1<<20) {
			continue
		}
		largeFresh = largeFresh || !f.Replace && f.Size >= 8<<20
		indices = append(indices, i)
		requests = append(requests, [4]any{i, base64.StdEncoding.EncodeToString([]byte(f.Remote)), f.Size, f.Replace && o.Overwrite})
	}
	if len(indices) < 32 && !largeFresh {
		return nil
	}
	ctx, cancel := context.WithCancel(s.externalContext())
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	var cmd *exec.Cmd
	if s.uploadCommand != nil {
		cmd = s.uploadCommand(ctx)
	} else {
		if s.cmd == nil || s.cmd.Path != "/usr/bin/ssh" || !validTarget(s.host.Target) {
			return nil
		}
		cmd = exec.CommandContext(ctx, "/usr/bin/ssh", append(rsyncSSHArgs(s.host), "--", s.host.Target, "python3 -c "+shellQuote(batchUploadPython))...)
	}
	canonical, err := s.Realpath(plan.Destination)
	if err != nil {
		return err
	}
	manifest, err := json.Marshal(struct {
		Root  [2]string `json:"root"`
		Files [][4]any  `json:"files"`
	}{[2]string{base64.StdEncoding.EncodeToString([]byte(plan.Destination)), base64.StdEncoding.EncodeToString([]byte(canonical))}, requests})
	if err != nil {
		return err
	}
	if len(manifest) > 32<<20 {
		return nil
	}
	diagnostics := &sshDiagnostics{}
	cmd.Stderr = diagnostics
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil
	}
	// EOF lets the remote helper clean staged files in its finally block.
	// Bound shutdown if a broken helper ignores its input or hangs in the OS.
	cmd.Cancel = func() error { return input.Close() }
	cmd.WaitDelay = 5 * time.Second
	if err = cmd.Start(); err != nil {
		input.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	timeout := s.timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	timer := time.AfterFunc(timeout, cancel)
	defer timer.Stop()
	reader := bufio.NewReader(output)
	var magic [8]byte
	if _, err = io.ReadFull(reader, magic[:]); err != nil || string(magic[:]) != "HOPUPG1\n" {
		input.Close()
		cancel()
		cmd.Wait()
		if s.externalContext().Err() != nil {
			return s.externalContext().Err()
		}
		return nil // No remote writes before capability negotiation.
	}
	p.setBackend("Stream")
	type sentFile struct {
		index    int
		progress *fileProgress
	}
	sent := make(chan sentFile, 64)
	sendResult := make(chan error, 1)
	go func() {
		defer close(sent)
		writer, _ := gzip.NewWriterLevel(input, gzip.BestSpeed)
		write := func() error {
			var length [4]byte
			binary.BigEndian.PutUint32(length[:], uint32(len(manifest)))
			if _, err := writer.Write(length[:]); err != nil {
				return err
			}
			if _, err := writer.Write(manifest); err != nil {
				return err
			}
			for _, i := range indices {
				if err := ctx.Err(); err != nil {
					return err
				}
				progress := p.startFile(plan.Files[i], false, plan.Destination)
				err := writeUploadStreamFile(ctx, writer, i, plan.Files[i], progress, func() { timer.Reset(timeout) })
				if err != nil {
					progress.complete(err)
					return err
				}
				select {
				case sent <- sentFile{i, progress}:
				case <-ctx.Done():
					progress.complete(ctx.Err())
					return ctx.Err()
				}
				// A flush retains the compression dictionary and allows the receiver to
				// commit without waiting for the next file or the producer's queue.
				if err = writer.Flush(); err != nil {
					return err
				}
			}
			if _, err := writer.Write([]byte{255, 255, 255, 255}); err != nil {
				return err
			}
			return writer.Close()
		}
		err := write()
		input.Close()
		sendResult <- err
		if err != nil {
			cancel()
		}
	}()
	var receiveErr error
	for file := range sent {
		if receiveErr == nil {
			var ack [4]byte
			_, receiveErr = io.ReadFull(reader, ack[:])
			if receiveErr == nil && binary.BigEndian.Uint32(ack[:]) != uint32(file.index) {
				receiveErr = errors.New("invalid upload acknowledgement")
			}
			if receiveErr != nil {
				cancel()
				input.Close()
			} else {
				completed[file.index] = true
				timer.Reset(timeout)
			}
		}
		file.progress.complete(receiveErr)
	}
	sendErr := <-sendResult
	if receiveErr == nil && sendErr == nil {
		var end [4]byte
		_, receiveErr = io.ReadFull(reader, end[:])
		if receiveErr == nil && binary.BigEndian.Uint32(end[:]) != 0xffffffff {
			receiveErr = errors.New("invalid upload completion")
		}
		if receiveErr == nil {
			_, receiveErr = reader.ReadByte()
			if receiveErr == io.EOF {
				receiveErr = nil
			} else if receiveErr == nil {
				receiveErr = errors.New("trailing upload acknowledgement")
			}
		}
	}
	if receiveErr != nil || sendErr != nil {
		cancel()
	}
	runErr := cmd.Wait()
	if sendErr != nil {
		return fmt.Errorf("batch upload stopped: %w%s", sendErr, diagnostics.suffix())
	}
	if receiveErr != nil {
		return fmt.Errorf("batch upload stopped: %w%s", receiveErr, diagnostics.suffix())
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if runErr != nil {
		return fmt.Errorf("batch upload stopped: %w%s", runErr, diagnostics.suffix())
	}
	for _, i := range indices {
		if !completed[i] {
			return errors.New("batch upload stopped before all files were committed")
		}
	}
	return nil
}

func writeUploadStreamFile(ctx context.Context, out io.Writer, index int, item PlannedFile, progress *fileProgress, active func()) error {
	f, err := os.Open(item.Local)
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || uint64(before.Size()) != item.Size {
		return errors.New("upload source changed")
	}
	var header [16]byte
	binary.BigEndian.PutUint32(header[:4], uint32(index))
	binary.BigEndian.PutUint64(header[4:12], item.Size)
	binary.BigEndian.PutUint32(header[12:], uint32(before.Mode().Perm()))
	if _, err = out.Write(header[:]); err != nil {
		return err
	}
	hash := sha256.New()
	buffer := make([]byte, min(item.Size, 256<<10))
	remaining := item.Size
	for remaining > 0 {
		if err = ctx.Err(); err != nil {
			return err
		}
		n := min(remaining, uint64(len(buffer)))
		if _, err = io.ReadFull(f, buffer[:n]); err != nil {
			return err
		}
		if _, err = out.Write(buffer[:n]); err != nil {
			return err
		}
		hash.Write(buffer[:n])
		remaining -= n
		progress.update(item.Size - remaining)
		active()
	}
	after, err := f.Stat()
	if err != nil {
		return err
	}
	current, err := os.Stat(item.Local)
	if err != nil || !os.SameFile(before, current) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) {
		return errors.New("upload source changed before verification")
	}
	_, err = out.Write(hash.Sum(nil))
	return err
}
