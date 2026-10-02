package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Block signatures bound memory and remote work. Transfers do not require any
// helper to be installed: an optional read-only command returns checksums.
// Basic SFTP servers always retain the full streaming transfer path.
type deltaManifest struct {
	blocks [][32]byte
	weak   []uint32
	whole  [32]byte
}

func deltaBlockSize(size uint64) uint64 {
	b := uint64(64 << 10)
	for (size+b-1)/b > 4096 {
		b *= 2
	}
	return b
}
func localManifest(f *os.File, size, block uint64) (deltaManifest, error) {
	var m deltaManifest
	whole := sha256.New()
	buf := make([]byte, min(uint64(256<<10), block))
	for off := uint64(0); off < size; off += block {
		part := sha256.New()
		n := min(block, size-off)
		if copied, err := io.CopyBuffer(io.MultiWriter(part, whole), io.NewSectionReader(f, int64(off), int64(n)), buf); err != nil {
			return m, err
		} else if uint64(copied) != n {
			return m, io.ErrUnexpectedEOF
		}
		var sum [32]byte
		copy(sum[:], part.Sum(nil))
		m.blocks = append(m.blocks, sum)
	}
	copy(m.whole[:], whole.Sum(nil))
	return m, nil
}

// Python is an optional single-process accelerator. The fallback uses POSIX
// sh/dd plus one of the common SHA-256 commands (GNU, BusyBox, BSD/macOS).
// Parameters are positional, never interpolated into the script.
const deltaHashScript = `set -eu
p=$1; size=$2; block=$3
if command -v python3 >/dev/null 2>&1; then
 exec python3 -c 'import sys,hashlib,zlib
p,size,block=sys.argv[1],int(sys.argv[2]),int(sys.argv[3])
h=hashlib.sha256()
with open(p,"rb") as f:
 step=block or 1048576
 for off in range(0,size,step):
  left=min(step,size-off); part=hashlib.sha256(); weak=1
  while left:
   b=f.read(min(left,262144))
   if not b: sys.exit(1)
   left-=len(b); h.update(b); part.update(b); weak=zlib.adler32(b,weak)
  if block: print(part.hexdigest(),"a32=%08x" % weak)
 if f.read(1): sys.exit(1)
print(h.hexdigest())' "$p" "$size" "$block"
fi
if command -v sha256sum >/dev/null 2>&1; then
 hash() { sha256sum; }
elif command -v shasum >/dev/null 2>&1; then
 hash() { shasum -a 256; }
elif command -v openssl >/dev/null 2>&1; then
 hash() { openssl dgst -sha256; }
else exit 127
fi
i=0; count=0
if [ "$block" -gt 0 ]; then count=$(( (size + block - 1) / block )); fi
while [ "$i" -lt "$count" ]; do
 dd if="$p" bs="$block" skip="$i" count=1 2>/dev/null | hash
 i=$((i+1))
done
hash < "$p"
`

// Restrict probes to real SSH transports. Tests inject the read-only runner.
func (s *SFTP) remoteManifest(h Host, p string, size, block uint64) (deltaManifest, error) {
	var m deltaManifest
	var out []byte
	var err error
	ctx, cancel := context.WithTimeout(s.externalContext(), 30*time.Second)
	defer cancel()
	if s.deltaHash != nil {
		out, err = s.deltaHash(ctx, p, size, block)
	} else {
		if s.cmd == nil || s.cmd.Path != "/usr/bin/ssh" || !validTarget(h.Target) || strings.Contains(h.Target, "/") || strings.ContainsRune(p, 0) {
			return m, errors.New("checksum helper unavailable")
		}
		command := "sh -c " + shellQuote(deltaHashScript) + " hop " + shellQuote(p) + " " + strconv.FormatUint(size, 10) + " " + strconv.FormatUint(block, 10)
		cmd := exec.CommandContext(ctx, "/usr/bin/ssh", append(rsyncSSHArgs(h), "--", h.Target, command)...)
		// Bound replies even if a remote shell prints unexpected output.
		var b limitedHashOutput
		cmd.Stdout = &b
		cmd.Stderr = io.Discard
		err = cmd.Run()
		out = b.Bytes()
	}
	if err != nil {
		return m, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	count := 0
	if block > 0 {
		count = int((size + block - 1) / block)
	}
	if len(lines) != count+1 {
		return m, errors.New("invalid checksum count")
	}
	for i, line := range lines {
		var digest []byte
		for _, field := range strings.Fields(line) {
			if len(field) == 64 {
				if d, e := hex.DecodeString(field); e == nil {
					digest = d
					break
				}
			}
		}
		if len(digest) != 32 {
			return m, errors.New("invalid checksum response")
		}
		var sum [32]byte
		copy(sum[:], digest)
		if i == len(lines)-1 {
			m.whole = sum
		} else {
			m.blocks = append(m.blocks, sum)
			for _, field := range strings.Fields(line) {
				if strings.HasPrefix(field, "a32=") {
					w, e := strconv.ParseUint(strings.TrimPrefix(field, "a32="), 16, 32)
					if e != nil {
						return m, errors.New("invalid rolling checksum")
					}
					m.weak = append(m.weak, uint32(w))
				}
			}
		}
	}
	return m, nil
}

type limitedHashOutput struct{ bytes.Buffer }

func (b *limitedHashOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("checksum output too large")
	}
	return b.Buffer.Write(p)
}
func nativeDeltaEnabled(size uint64) bool {
	backend := os.Getenv("HOP_TRANSFER_BACKEND")
	return size >= 1<<20 && backend != "sftp" && backend != "rsync"
}

// The cheap first pass matches fixed blocks at any block offset. A rolling
// scan can recover matches across insertions when weak signatures are available.
type deltaRange struct {
	off, from, n uint64
	reuse        bool
}

func deltaRanges(want, basis deltaManifest, size, basisSize, block uint64) ([]deltaRange, uint64) {
	index := make(map[[32]byte]uint64, len(basis.blocks))
	for i, sum := range basis.blocks {
		if uint64(i+1)*block <= basisSize {
			index[sum] = uint64(i) * block
		}
	}
	var ranges []deltaRange
	var reused uint64
	for i, sum := range want.blocks {
		off := uint64(i) * block
		n := min(block, size-off)
		from, ok := index[sum]
		ok = ok && n == block
		if i < len(basis.blocks) && sum == basis.blocks[i] && n == min(block, basisSize-min(off, basisSize)) {
			from = off
			ok = true
		}
		r := deltaRange{off, from, n, ok}
		if ok {
			reused += n
		}
		if len(ranges) > 0 {
			last := &ranges[len(ranges)-1]
			if last.reuse == ok && last.off+last.n == off && (!ok || last.from+last.n == from) {
				last.n += n
				continue
			}
		}
		ranges = append(ranges, r)
	}
	return ranges, reused
}

func (s *SFTP) deltaUpload(h Host, f *os.File, temp, basis string, size, basisSize uint64, mode uint32, progress func(uint64)) (bool, error) {
	if !nativeDeltaEnabled(size) || s.ext["copy-data"] != "1" {
		return false, nil
	}
	block := deltaBlockSize(max(size, basisSize))
	old, err := s.remoteManifest(h, basis, basisSize, block)
	if err != nil {
		return false, nil
	}
	want, err := localManifest(f, size, block)
	if err != nil {
		return true, err
	}
	ranges, reused := deltaRanges(want, old, size, basisSize, block)
	if canRoll(old, size, reused, block) {
		rr, rb, e := rollingUploadRanges(s.externalContext(), f, size, basisSize, block, old)
		if e != nil {
			return true, e
		}
		if rb > reused {
			ranges, reused = rr, rb
		}
	}

	if reused < size/2 {
		return false, nil
	}
	src, err := s.open(basis, 1, 0)
	if err != nil {
		return true, err
	}
	defer s.closeHandle(src)
	dst, err := s.open(temp, 2|8|32, mode)
	if err != nil {
		return true, err
	}
	err = s.deltaWriteRanges(f, src, dst, ranges, progress)
	ce := s.closeHandle(dst)
	if err != nil {
		return true, err
	}
	if ce != nil {
		return true, ce
	}
	// Hash the assembled file, not the earlier basis: catches modifications
	// during reuse, unexpected server copies and local source changes.
	actual, err := s.remoteManifest(h, temp, size, 0)
	if err != nil {
		return true, fmt.Errorf("verify delta upload: %w", err)
	}
	if actual.whole != want.whole {
		return true, errors.New("delta upload checksum mismatch; destination was not committed")
	}
	s.deltaTransfers.Add(1)
	s.deltaReused.Add(reused)
	return true, nil
}
func (s *SFTP) deltaWriteRanges(f *os.File, src, dst string, ranges []deltaRange, progress func(uint64)) error {
	chunk, window, err := s.transferGeometry(dst, true)
	if err != nil {
		return err
	}
	timer := time.AfterFunc(s.timeout, s.Abort)
	defer timer.Stop()
	type pendingWrite struct {
		reply <-chan sftpReply
		n     uint64
	}
	pending := make([]pendingWrite, 0, window)
	var first error
	var done uint64
	drain := func() {
		w := pending[0]
		pending = pending[1:]
		r := <-w.reply
		if r.err == nil && r.typ != fxStatus {
			r.err = errors.New("invalid delta write response")
		}
		if r.err != nil && first == nil {
			first = r.err
		}
		if r.err == nil {
			done += w.n
			if progress != nil {
				progress(done)
			}
		}
	}
	buf := make([]byte, chunk)
	for _, r := range ranges {
		for off := uint64(0); off < r.n && first == nil; {
			n := min(uint64(chunk), r.n-off)
			var reply <-chan sftpReply
			if r.reuse {
				// Bound each server-side operation, including its timeout, to 8 MiB.
				n = min(uint64(8<<20), r.n-off)
				reply = s.startActiveRequest(fxExtended, func() { timer.Reset(s.timeout) }, str("copy-data"), str(src), u64(r.from+off), u64(n), str(dst), u64(r.off+off))
			} else {
				if _, err := f.ReadAt(buf[:n], int64(r.off+off)); err != nil {
					first = err
					break
				}
				reply = s.startActiveRequest(fxWrite, func() { timer.Reset(s.timeout) }, str(dst), u64(r.off+off), u32(uint32(n)), buf[:n])
			}
			pending = append(pending, pendingWrite{reply, n})
			off += n
			if len(pending) >= window {
				drain()
			}
		}
	}
	for len(pending) > 0 {
		drain()
	}
	return first
}

func (s *SFTP) deltaDownload(h Host, remote, basis string, f *os.File, size, basisSize uint64, progress func(uint64)) (bool, error) {
	if !nativeDeltaEnabled(size) {
		return false, nil
	}
	block := deltaBlockSize(max(size, basisSize))
	want, err := s.remoteManifest(h, remote, size, block)
	if err != nil {
		return false, nil
	}
	oldFile, err := os.Open(basis)
	if err != nil {
		return true, err
	}
	defer oldFile.Close()
	old, err := localManifest(oldFile, basisSize, block)
	if err != nil {
		return true, err
	}
	ranges, reused := deltaRanges(want, old, size, basisSize, block)
	if canRoll(want, size, reused, block) {
		rr, rb, e := rollingDownloadRanges(s.externalContext(), oldFile, size, basisSize, block, want, old)
		if e != nil {
			return true, e
		}
		if rb > reused {
			ranges, reused = rr, rb
		}
	}

	if reused < size/2 {
		return false, nil
	}
	src, err := s.open(remote, 1, 0)
	if err != nil {
		return true, err
	}
	defer s.closeHandle(src)
	chunk, window, err := s.transferGeometry(src, false)
	if err != nil {
		return true, err
	}
	// Workers pull bounded chunks without allocating a job per chunk of a huge file.
	var mu sync.Mutex
	rangeIndex := 0
	var rangeOffset, done uint64
	var failed bool
	workers := min(window, 32)
	err = parallelFilesN(workers, workers, func(_ int) (workerErr error) {
		defer func() {
			if workerErr != nil {
				mu.Lock()
				failed = true
				mu.Unlock()
			}
		}()
		buf := make([]byte, chunk)
		for {
			mu.Lock()
			if failed || rangeIndex == len(ranges) {
				mu.Unlock()
				return nil
			}
			r := ranges[rangeIndex]
			n := min(uint64(chunk), r.n-rangeOffset)
			r.off += rangeOffset
			r.from += rangeOffset
			r.n = n
			rangeOffset += n
			if rangeOffset == ranges[rangeIndex].n {
				rangeIndex++
				rangeOffset = 0
			}
			mu.Unlock()
			b := buf[:n]
			if r.reuse {
				if _, e := oldFile.ReadAt(b, int64(r.from)); e != nil {
					return e
				}
			} else {
				for n := 0; n < len(b); {
					data, e := s.request(fxRead, fields(str(src), u64(r.off+uint64(n)), u32(uint32(len(b)-n))), fxData)
					if e != nil {
						return e
					}
					d := decoder{b: data}
					part := d.take(int(d.u32()))
					if d.err != nil || len(part) == 0 || len(part) > len(b)-n {
						return errors.New("invalid delta read length")
					}
					n += copy(b[n:], part)
				}
			}
			if n, e := f.WriteAt(b, int64(r.off)); e != nil {
				return e
			} else if n != len(b) {
				return io.ErrShortWrite
			}
			mu.Lock()
			done += r.n
			if progress != nil {
				progress(done)
			}
			mu.Unlock()
		}
	})
	if err != nil {
		return true, err
	}
	// Verify reused bytes too, including a local basis edited during copying.
	actual, err := localManifest(f, size, block)
	if err != nil {
		return true, err
	}
	if actual.whole != want.whole {
		return true, errors.New("delta download checksum mismatch; destination was not committed")
	}
	s.deltaTransfers.Add(1)
	s.deltaReused.Add(reused)
	return true, nil
}
