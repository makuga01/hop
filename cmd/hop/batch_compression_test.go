package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBatchCompression(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3")
	}
	for _, codec := range []string{"gzip", "native", "command", "unexecutable", "failed"} {
		t.Run(codec, func(t *testing.T) {
			script := batchCompressionPython
			if codec != "native" {
				script += "\ndef unavailable(): raise ImportError('fixture')\nNativeZstdOutput=unavailable\n"
			} else if err := exec.Command(python, "-c", "from compression.zstd import ZstdCompressor").Run(); err != nil {
				t.Skip("Python native zstd unavailable")
			}
			binary := ""
			switch codec {
			case "command":
				binary, err = exec.LookPath("zstd")
				if err != nil {
					t.Skip("zstd command unavailable")
				}
			case "unexecutable", "failed":
				binary = filepath.Join(t.TempDir(), "compressor")
				mode := os.FileMode(0600)
				if codec == "failed" {
					mode = 0700
				}
				if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 7\n"), mode); err != nil {
					t.Fatal(err)
				}
			}
			script += "\nshutil.which=lambda name: " + strconv.Quote(binary) + "\nout=batch_output()\nout.write(b'verified '*10000)\nout.flush()\nout.write(b'end')\nout.close()\n"
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(python, "-c", script)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if codec == "failed" {
				if err == nil {
					t.Fatal("failed compressor accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err, stderr.String())
			}
			wire := stdout.Bytes()
			magic := "HOPFILEZ1\n"
			if codec == "gzip" || codec == "unexecutable" {
				magic = "HOPFILES1\n"
			}
			if len(wire) < len(magic) || string(wire[:len(magic)]) != magic {
				t.Fatal("wrong codec selected")
			}
			for _, truncated := range []bool{false, true} {
				payload := wire[len(magic):]
				if truncated {
					payload = payload[:len(payload)/2]
				}
				r, err := batchDecompressor(magic, bytes.NewReader(payload))
				var got []byte
				if err == nil {
					got, err = io.ReadAll(r)
					r.Close()
				}
				if truncated {
					if err == nil {
						t.Fatal("truncated compression accepted")
					}
				} else if err != nil || string(got) != strings.Repeat("verified ", 10000)+"end" {
					t.Fatal("compression round trip failed", err)
				}
			}
		})
	}
}

func TestBatchCompressionWindowBound(t *testing.T) {
	// Empty Zstandard frames with an explicit window descriptor. A peer may
	// request the supported 64 MiB history, but cannot force unbounded memory.
	for _, tc := range []struct {
		descriptor byte
		valid      bool
	}{{0x60, true}, {0x80, true}, {0x88, false}} {
		wire := []byte{0x28, 0xb5, 0x2f, 0xfd, 0, tc.descriptor, 1, 0, 0}
		r, err := batchDecompressor("HOPFILEZ1\n", bytes.NewReader(wire))
		if err == nil {
			_, err = io.ReadAll(r)
			r.Close()
		}
		if (err == nil) != tc.valid {
			t.Fatalf("window descriptor %x: %v", tc.descriptor, err)
		}
	}
}
