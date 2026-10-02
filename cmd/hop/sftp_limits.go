package main

import (
	"errors"
	"fmt"
)

// https://github.com/openssh/openssh-portable/blob/master/PROTOCOL section 4.8.
// Unknown servers retain the conservative 32 KiB / 64-request defaults.
type transferLimits struct {
	packet, read, write, handles uint64
	negotiated                   bool
}

func (s *SFTP) serverLimits() (transferLimits, error) {
	s.limitsOnce.Do(func() {
		if s.ext["limits@openssh.com"] != "1" {
			return
		}
		b, err := s.request(fxExtended, str("limits@openssh.com"), 201)
		if err != nil {
			var status *StatusError
			if errors.As(err, &status) && status.Code == 8 {
				return
			}
			s.limitsErr = err
			return
		}
		d := decoder{b: b}
		s.limits = transferLimits{packet: d.u64(), read: d.u64(), write: d.u64(), handles: d.u64(), negotiated: true}
		if d.err != nil || d.remaining() != 0 {
			s.limitsErr = errors.New("invalid SFTP server limits")
		}
	})
	return s.limits, s.limitsErr
}
func (s *SFTP) fileWorkers() (int, error) {
	limits, err := s.serverLimits()
	if err != nil {
		return 0, err
	}
	if !limits.negotiated {
		return batchWorkers, nil
	}
	workers := uint64(32)
	// Leave headroom for handles used by the server and the browser.
	if limits.handles != 0 {
		workers = min(workers, max(uint64(1), limits.handles/2))
	}
	return int(workers), nil
}
func (s *SFTP) transferGeometry(handle string, upload bool) (int, int, error) {
	limits, err := s.serverLimits()
	if err != nil {
		return 0, 0, err
	}
	if !limits.negotiated {
		return transferChunk, transferWindow, nil
	}
	chunk := uint64(256 * 1024)
	maxData, overhead := limits.read, uint64(13)
	if upload {
		maxData, overhead = limits.write, uint64(25+len(handle))
	}
	if maxData != 0 {
		chunk = min(chunk, maxData)
	}
	if limits.packet != 0 {
		if limits.packet <= uint64(25+len(handle)) {
			return 0, 0, fmt.Errorf("SFTP packet limit too small for file handle")
		}
		chunk = min(chunk, limits.packet-overhead)
	}
	// Cap both bytes and request count, even for very small server limits.
	window := min(uint64(256), max(uint64(1), (8<<20)/chunk))
	return int(chunk), int(window), nil
}
