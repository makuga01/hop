package main

import (
	"errors"
	"fmt"
	"path"
	"sync"
)

type remoteDirectoryScan struct {
	real     string
	children []Entry
}
type remoteTreeScan struct {
	readChecked bool
	attrs       map[string]Attr
	dirs        map[string]remoteDirectoryScan
}
type remoteScanNode struct {
	name      string
	attr      Attr
	depth     int
	ancestors map[string]bool
}

// Browse one tree level concurrently. Immutable ancestor sets keep independent
// branches separate while detecting directory symlink cycles before copying.
func scanRemoteTree(s *SFTP, roots []FileItem, known map[string]Attr, workers int, scan *scanProgress) (remoteTreeScan, error) {
	result := remoteTreeScan{attrs: map[string]Attr{}, dirs: map[string]remoteDirectoryScan{}}
	type listing struct {
		done     chan struct{}
		children []Entry
		err      error
	}
	var mu sync.Mutex
	listings := map[string]*listing{}
	readDirectory := func(real, name string) ([]Entry, error) {
		mu.Lock()
		cached, ok := listings[real]
		if !ok {
			cached = &listing{done: make(chan struct{})}
			listings[real] = cached
		}
		mu.Unlock()
		if ok {
			<-cached.done
			return cached.children, cached.err
		}
		cached.children, cached.err = s.ReadDir(name)
		close(cached.done)
		return cached.children, cached.err
	}
	nodes := make([]remoteScanNode, len(roots))
	for i, r := range roots {
		nodes[i] = remoteScanNode{name: r.Path, attr: known[r.Path]}
	}
	entries := len(nodes)
	for len(nodes) > 0 {
		dirs := make([]remoteDirectoryScan, len(nodes))
		attrs := make([]Attr, len(nodes))
		e := parallelFilesN(len(nodes), min(workers, 64), func(i int) error {
			n := nodes[i]
			if n.depth > 128 {
				return errors.New("tree exceeds 128 levels; select a smaller directory")
			}
			a := n.attr
			if a.Flags&5 != 5 || (!a.Regular() && !a.Dir()) {
				var e error
				a, e = s.Stat(n.name, true)
				if e != nil {
					return e
				}
			}
			if !a.Regular() && !a.Dir() {
				return fmt.Errorf("%s is a special file; these are not supported in recursive copies (nothing copied)", safeText(n.name))
			}
			scan.found(a.Dir())
			attrs[i] = a
			if !a.Dir() {
				return nil
			}
			real, e := s.Realpath(n.name)
			if e != nil {
				return e
			}
			if n.ancestors[real] {
				return fmt.Errorf("symlink loop at %s (nothing copied)", safeText(n.name))
			}
			children, e := readDirectory(real, n.name)
			if e != nil {
				return fmt.Errorf("cannot read directory %s: %w (nothing copied)", safeText(n.name), e)
			}
			dirs[i] = remoteDirectoryScan{real, children}
			return nil
		})
		if e != nil {
			return result, e
		}
		var next []remoteScanNode
		for i, n := range nodes {
			result.attrs[n.name] = attrs[i]
			if !attrs[i].Dir() {
				continue
			}
			d := dirs[i]
			result.dirs[n.name] = d
			ancestors := make(map[string]bool, len(n.ancestors)+1)
			for k := range n.ancestors {
				ancestors[k] = true
			}
			ancestors[d.real] = true
			entries += len(d.children)
			if entries > 100000 {
				return result, errors.New("tree exceeds 100,000 entries; select a smaller directory")
			}
			for _, child := range d.children {
				next = append(next, remoteScanNode{path.Join(n.name, child.Name), child.Attr, n.depth + 1, ancestors})
			}
		}
		nodes = next
	}
	return result, nil
}

// Planning holds small metadata buffers, unlike multi-MiB transfer windows.
// Respect the server's handle budget while overlapping permission checks.
func (s *SFTP) planningWorkers() (int, error) {
	limits, e := s.serverLimits()
	if e != nil {
		return 0, e
	}
	if !limits.negotiated {
		return batchWorkers, nil
	}
	workers := uint64(256)
	if limits.handles != 0 {
		workers = min(workers, max(uint64(1), limits.handles/2))
	}
	return int(workers), nil
}
