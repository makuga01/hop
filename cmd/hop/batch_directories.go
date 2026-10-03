package main

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// Independent siblings can run together; parents must precede children when
// creating, and follow children when applying potentially read-only modes.
func directoryLayers(dirs []PlannedDirectory, get bool) [][]int {
	byDepth := map[int][]int{}
	depths := []int{}
	for i, d := range dirs {
		name := d.Remote
		if get {
			name = d.Local
		}
		depth := strings.Count(path.Clean(name), "/")
		if _, ok := byDepth[depth]; !ok {
			depths = append(depths, depth)
		}
		byDepth[depth] = append(byDepth[depth], i)
	}
	sort.Ints(depths)
	layers := make([][]int, 0, len(depths))
	for _, depth := range depths {
		layers = append(layers, byDepth[depth])
	}
	return layers
}

func prepareDirectories(s *SFTP, dirs []PlannedDirectory, get bool, status *activity, p *batchProgress) ([]PlannedDirectory, error) {
	made := make([]bool, len(dirs))
	done := 0
	last := time.Time{}
	var mu sync.Mutex
	status.Phase(fmt.Sprintf("Preparing folders · 0/%d", len(dirs)))
	workers := 8
	if !get {
		var err error
		workers, err = s.fileWorkers()
		if err != nil {
			return nil, err
		}
	}
	for _, layer := range directoryLayers(dirs, get) {
		if err := parallelFilesN(len(layer), workers, func(n int) error {
			i := layer[n]
			created, err := ensureDirectory(s, dirs[i], get)
			if err != nil {
				return err
			}
			made[i] = created
			if created {
				name := dirs[i].Remote
				if get {
					name = dirs[i].Local
				}
				p.folder(name)
			}
			mu.Lock()
			defer mu.Unlock()
			done++
			if time.Since(last) >= 100*time.Millisecond || done == len(dirs) {
				status.Phase(fmt.Sprintf("Preparing folders · %d/%d", done, len(dirs)))
				last = time.Now()
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	created := []PlannedDirectory{}
	for i, d := range dirs {
		if made[i] {
			created = append(created, d)
		}
	}
	return created, nil
}

func finishDirectories(s *SFTP, dirs []PlannedDirectory, get bool, status *activity) error {
	layers := directoryLayers(dirs, get)
	workers := 8
	if !get {
		var err error
		workers, err = s.fileWorkers()
		if err != nil {
			return err
		}
	}
	done := 0
	var mu sync.Mutex
	last := time.Time{}
	for level := len(layers) - 1; level >= 0; level-- {
		layer := layers[level]
		if err := parallelFilesN(len(layer), workers, func(n int) error {
			d := dirs[layer[n]]
			var err error
			if get {
				err = os.Chmod(d.Local, os.FileMode(d.Mode))
			} else {
				err = s.Chmod(d.Remote, d.Mode)
			}
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			done++
			if time.Since(last) >= 100*time.Millisecond || done == len(dirs) {
				status.Phase(fmt.Sprintf("Finishing folder permissions · %d/%d", done, len(dirs)))
				last = time.Now()
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
