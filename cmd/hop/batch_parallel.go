package main

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"
)

const batchWorkers = 8

// Stop scheduling on the first error, then let in-flight files finish safely.
// In particular, a failed move must never proceed to remove source files.
func parallelFiles(count int, fn func(int) error) error {
	return parallelFilesN(count, batchWorkers, fn)
}

func parallelFilesN(count, workers int, fn func(int) error) error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	next := 0
	var first error
	for w := 0; w < min(workers, count); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if first != nil || next == count {
					mu.Unlock()
					return
				}
				i := next
				next++
				mu.Unlock()
				if err := fn(i); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return first
}

func copyPlannedFiles(s *SFTP, h Host, plan CopyPlan, get bool, o Options, p *batchProgress) error {
	if p == nil {
		p = &batchProgress{embedded: true, total: plan.Total, files: len(plan.Files), started: time.Now()}
		p.console = startActivity("Copying files", plan.Total, true)
		defer p.console.Stop()
	}
	o.Progress = p
	o.deferHistory = true
	completed := make([]bool, len(plan.Files))
	workers, err := s.fileWorkers()
	if err != nil {
		return err
	}
	// Avoid giving dozens of large files their own multi-MiB transfer window.
	for _, f := range plan.Files {
		if f.Size > 1<<20 {
			workers = min(workers, batchWorkers)
			break
		}
	}
	handled, err := tryRsyncBatch(s, h, plan, get, o, p, completed)
	if !handled {
		err = parallelFilesN(len(plan.Files), workers, func(i int) error {
			f := plan.Files[i]
			options := o
			options.exactDestination = true
			options.fileProgress = p.startFile(f, get, plan.Destination)
			var err error
			if get {
				err = getFile(s, h, f.Remote, f.Local, options)
			} else {
				err = sendFile(s, h, f.Local, f.Remote, options)
			}
			options.fileProgress.complete(err)
			completed[i] = err == nil
			if err != nil {
				return fmt.Errorf("%s: %w", safeText(filepath.Base(f.Local)), err)
			}
			return nil
		})
	}
	// Save once per batch instead of fsyncing and rewriting history per tiny file.
	count := 0
	for _, ok := range completed {
		if ok {
			count++
		}
	}
	if count > 0 {
		direction := "send"
		if get {
			direction = "get"
		}
		saveErr := updateState(func(state *State) {
			transfers := make([]Transfer, 0, min(count, 200))
			for i := len(plan.Files) - 1; i >= 0 && len(transfers) < 200; i-- {
				if !completed[i] {
					continue
				}
				f := plan.Files[i]
				abs, _ := filepath.Abs(f.Local)
				transfers = append(transfers, Transfer{h.Key(), f.Remote, filepath.Dir(abs), filepath.Base(abs), direction, time.Now().Unix()})
			}
			state.Transfers = append(transfers, state.Transfers...)
			if len(state.Transfers) > 200 {
				state.Transfers = state.Transfers[:200]
			}
		})
		if saveErr != nil {
			o.warning("Could not save transfer history: " + saveErr.Error())
		}
	}
	if err != nil {
		return fmt.Errorf("%d/%d files completed; %w", count, len(plan.Files), err)
	}
	return nil
}
