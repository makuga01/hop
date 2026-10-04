package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestStreamBatchDurabilityBarrier(t *testing.T) {
	for _, failure := range []string{"", "data", "device", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := t.TempDir()
			files := make([]*pendingStreamFile, 4)
			for i := range files {
				p := &pendingStreamFile{to: filepath.Join(root, fmt.Sprint(i)), data: []byte("verified payload"), mode: 0640}
				files[i] = p
				defer p.cleanup()
				if err := p.prepare(); err != nil {
					t.Fatal(err)
				}
			}
			var dataCalls atomic.Int32
			deviceCalls := 0
			injected := errors.New("injected sync failure")
			err := syncStreamBatchDarwin(ctx, files, func(f *os.File) error {
				n := dataCalls.Add(1)
				if failure == "data" {
					return injected
				}
				if failure == "cancel" && n == int32(len(files)) {
					cancel()
				}
				return syncStreamData(f)
			}, func(f *os.File) error {
				deviceCalls++
				if dataCalls.Load() != int32(len(files)) {
					t.Error("device barrier ran before all file syncs")
				}
				for _, p := range files {
					if _, err := os.Lstat(p.to); !os.IsNotExist(err) {
						t.Error("file published before barrier")
					}
				}
				if failure == "device" {
					return injected
				}
				return f.Sync()
			})
			if failure == "" {
				if err != nil || deviceCalls != 1 {
					t.Fatalf("barrier count=%d, error=%v", deviceCalls, err)
				}
				for _, p := range files {
					if err := p.publish(); err != nil {
						t.Fatal(err)
					}
					got, err := os.ReadFile(p.to)
					if err != nil || string(got) != "verified payload" {
						t.Fatal("content mismatch", err)
					}
				}
			} else {
				if err == nil {
					t.Fatal("sync failure was ignored")
				}
				for _, p := range files {
					if p.synced || p.publish() == nil {
						t.Fatal("failed batch can be published")
					}
					p.cleanup()
				}
				entries, _ := os.ReadDir(root)
				if len(entries) != 0 {
					t.Fatal("failed batch left files behind")
				}
			}
		})
	}
}
