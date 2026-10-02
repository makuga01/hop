package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"hash/adler32"
	"io"
	"os"
)

// Adler-32 only finds candidate windows. Every match is confirmed with SHA-256,
// and the assembled file is independently verified before commit.
func rollAdler(sum uint32, old, next byte, n uint64) uint32 {
	const mod = int64(65521)
	a := (int64(sum&65535) - int64(old) + int64(next) + mod) % mod
	b := (int64(sum>>16) - int64(n)*int64(old) + a - 1) % mod
	if b < 0 {
		b += mod
	}
	return uint32(b<<16 | a)
}
func scanDeltaMatches(ctx context.Context, f *os.File, size, remoteSize, block uint64, m deltaManifest, emit func(uint64, int)) error {
	candidates := map[uint32][]int{}
	for i, w := range m.weak {
		if uint64(i+1)*block <= remoteSize {
			candidates[w] = append(candidates[w], i)
		}
	}
	if len(candidates) == 0 || size < block {
		return nil
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(f, 0, int64(size)), 256<<10)
	ring := make([]byte, block)
	if _, e := io.ReadFull(reader, ring); e != nil {
		return e
	}
	sum := adler32.Checksum(ring)
	head := 0
	var nextCheck uint64
	for off := uint64(0); off+block <= size; {
		if off >= nextCheck {
			nextCheck = off + 65536
			if e := ctx.Err(); e != nil {
				return e
			}
		}
		match := -1
		if ids := candidates[sum]; len(ids) > 0 {
			hash := sha256.New()
			hash.Write(ring[head:])
			hash.Write(ring[:head])
			var strong [32]byte
			copy(strong[:], hash.Sum(nil))
			for _, i := range ids {
				if strong == m.blocks[i] {
					match = i
					break
				}
			}
		}
		if match >= 0 {
			emit(off, match)
			off += block
			if off+block > size {
				break
			}
			if _, e := io.ReadFull(reader, ring); e != nil {
				return e
			}
			head = 0
			sum = adler32.Checksum(ring)
		} else {
			if off+block == size {
				break
			}
			next, e := reader.ReadByte()
			if e != nil {
				return e
			}
			sum = rollAdler(sum, ring[head], next, block)
			ring[head] = next
			head = (head + 1) % len(ring)
			off++
		}
	}
	return nil
}
func appendDeltaRange(ranges []deltaRange, r deltaRange) []deltaRange {
	if r.n == 0 {
		return ranges
	}
	if len(ranges) > 0 {
		last := &ranges[len(ranges)-1]
		if last.reuse == r.reuse && last.off+last.n == r.off && (!r.reuse || last.from+last.n == r.from) {
			last.n += r.n
			return ranges
		}
	}
	return append(ranges, r)
}
func rollingUploadRanges(ctx context.Context, f *os.File, size, basisSize, block uint64, old deltaManifest) ([]deltaRange, uint64, error) {
	var ranges []deltaRange
	var cursor, reused uint64
	e := scanDeltaMatches(ctx, f, size, basisSize, block, old, func(off uint64, i int) {
		ranges = appendDeltaRange(ranges, deltaRange{off: cursor, n: off - cursor})
		ranges = appendDeltaRange(ranges, deltaRange{off: off, from: uint64(i) * block, n: block, reuse: true})
		cursor = off + block
		reused += block
	})
	ranges = appendDeltaRange(ranges, deltaRange{off: cursor, n: size - cursor})
	return ranges, reused, e
}
func rollingDownloadRanges(ctx context.Context, f *os.File, size, basisSize, block uint64, want, old deltaManifest) ([]deltaRange, uint64, error) {
	found := map[[32]byte]uint64{}
	e := scanDeltaMatches(ctx, f, basisSize, size, block, want, func(off uint64, i int) { found[want.blocks[i]] = off })
	if e != nil {
		return nil, 0, e
	}
	var ranges []deltaRange
	var reused uint64
	for i, sum := range want.blocks {
		off := uint64(i) * block
		n := min(block, size-off)
		from, ok := found[sum]
		ok = ok && n == block
		if !ok && i < len(old.blocks) && sum == old.blocks[i] && n == min(block, basisSize-min(off, basisSize)) {
			from = off
			ok = true
		}
		ranges = appendDeltaRange(ranges, deltaRange{off: off, from: from, n: n, reuse: ok})
		if ok {
			reused += n
		}
	}
	return ranges, reused, nil
}
func canRoll(m deltaManifest, size, reused, block uint64) bool {
	// Limit scan memory and avoid scanning already-near-identical files twice.
	return len(m.weak) == len(m.blocks) && len(m.weak) > 0 && block <= 8<<20 && reused < size-size/10
}
