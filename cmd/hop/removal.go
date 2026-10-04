package main

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

type removalEntry struct {
	name     string
	attr     Attr
	local    os.FileInfo
	children []string
}

func removalStat(s *SFTP, name string, remote bool) (removalEntry, error) {
	r := removalEntry{name: name}
	if remote {
		a, e := s.Stat(name, false)
		r.attr = a
		return r, e
	}
	info, e := os.Lstat(name)
	if e != nil {
		return r, e
	}
	r.local = info
	mode := uint32(info.Mode().Perm())
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		mode |= 0120000
	case info.IsDir():
		mode |= 0040000
	case info.Mode().IsRegular():
		mode |= 0100000
	}
	r.attr = Attr{Mode: mode, Size: uint64(info.Size()), Mtime: uint32(info.ModTime().Unix())}
	return r, nil
}
func removalChildren(s *SFTP, name string, remote bool) ([]string, error) {
	names := []string{}
	if remote {
		entries, e := s.ReadDir(name)
		if e != nil {
			return nil, e
		}
		for _, f := range entries {
			names = append(names, f.Name)
		}
	} else {
		entries, e := os.ReadDir(name)
		if e != nil {
			return nil, e
		}
		for _, f := range entries {
			names = append(names, f.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Record a postorder tree without following links. Every removal is non-recursive.
func planRemoval(ctx context.Context, s *SFTP, files []FileItem, remote bool) ([]removalEntry, error) {
	return planRemovalObserved(ctx, s, files, remote, nil)
}

func planRemovalObserved(ctx context.Context, s *SFTP, files []FileItem, remote bool, progress *scanProgress) ([]removalEntry, error) {
	progress.setPhase("Scanning selection for deletion", 0)
	if remote {
		entries, err := s.scanRemovalWithHelper(ctx, files, progress)
		if err == nil {
			return entries, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if progress != nil {
			progress.mu.Lock()
			progress.files = 0
			progress.dirs = 0
			progress.mu.Unlock()
		}
	}
	entries := []removalEntry{}
	seen := map[string]bool{}
	var walk func(string, int, *Attr) error
	walk = func(name string, depth int, known *Attr) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		name = path.Clean(name)
		if !path.IsAbs(name) || name == "/" {
			return fmt.Errorf("refusing to delete filesystem root or relative path: %s", safeText(name))
		}
		if seen[name] {
			return nil
		}
		seen[name] = true
		if depth > 128 || len(seen) > 100000 {
			return fmt.Errorf("selection exceeds 128 levels or 100,000 entries")
		}
		var r removalEntry
		var e error
		if remote && known != nil && known.Flags&13 == 13 {
			r = removalEntry{name: name, attr: *known}
		} else {
			r, e = removalStat(s, name, remote)
		}
		if e != nil {
			return e
		}
		progress.found(r.attr.Dir())
		if r.attr.Dir() {
			attrs := map[string]Attr{}
			if remote {
				var children []Entry
				children, e = s.ReadDir(name)
				for _, child := range children {
					r.children = append(r.children, child.Name)
					attrs[child.Name] = child.Attr
				}
				sort.Strings(r.children)
			} else {
				r.children, e = removalChildren(s, name, false)
			}
			if e != nil {
				return e
			}
			for _, child := range r.children {
				if child == "." || child == ".." || strings.Contains(child, "/") {
					return fmt.Errorf("invalid directory entry")
				}
				a := attrs[child]
				if e = walk(path.Join(name, child), depth+1, &a); e != nil {
					return e
				}
			}
		}
		entries = append(entries, r)
		return nil
	}
	for _, f := range files {
		if e := walk(f.Path, 0, nil); e != nil {
			return nil, e
		}
	}
	return entries, nil
}
func unchangedRemoval(s *SFTP, want removalEntry, remote, full bool) error {
	got, e := removalStat(s, want.name, remote)
	if e != nil {
		return e
	}
	same := got.attr.Mode == want.attr.Mode
	if !remote {
		same = same && os.SameFile(want.local, got.local)
	}
	if full || !want.attr.Dir() {
		same = same && got.attr.Size == want.attr.Size && got.attr.Mtime == want.attr.Mtime
		if !remote {
			same = same && want.local.ModTime().Equal(got.local.ModTime())
		}
	}
	if !same {
		return fmt.Errorf("source changed; kept %s", safeText(want.name))
	}
	if full && want.attr.Dir() {
		names, e := removalChildren(s, want.name, remote)
		if e != nil {
			return e
		}
		if strings.Join(names, "\x00") != strings.Join(want.children, "\x00") {
			return fmt.Errorf("folder contents changed; kept %s", safeText(want.name))
		}
	}
	return nil
}
func executeRemoval(ctx context.Context, s *SFTP, entries []removalEntry, remote bool, p *batchProgress) error {
	// Validate the entire original selection before deleting any of it.
	// Pipelined read-only checks hide network latency without parallel deletion.
	workers := 1
	if remote {
		var err error
		workers, err = s.planningWorkers()
		workers = min(workers, batchWorkers)
		if err != nil {
			return err
		}
	}
	if p != nil {
		p.mu.Lock()
		p.removing, p.checking, p.finished = true, true, false
		p.total, p.completed, p.current = 0, 0, 0
		p.files, p.doneFiles, p.checked = len(entries), 0, 0
		p.phase, p.name, p.lastChange = "Checking selection before deletion", "", time.Now()
		p.mu.Unlock()
	}
	err := parallelFilesN(len(entries), workers, func(i int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		r := entries[i]
		if err := unchangedRemoval(s, r, remote, true); err != nil {
			return err
		}
		if p != nil {
			p.mu.Lock()
			p.checked++
			p.name = r.name
			p.lastChange = time.Now()
			p.mu.Unlock()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if p != nil {
		p.mu.Lock()
		p.checking = false
		p.phase = "Deleting"
		p.lastChange = time.Now()
		p.mu.Unlock()
	}
	dirs := map[string]removalEntry{}
	for _, r := range entries {
		if r.attr.Dir() {
			dirs[r.name] = r
		}
	}
	for i, r := range entries {
		if e := ctx.Err(); e != nil {
			return e
		}
		for parent := path.Dir(r.name); parent != "/"; parent = path.Dir(parent) {
			if a, ok := dirs[parent]; ok {
				if e := unchangedRemoval(s, a, remote, false); e != nil {
					return e
				}
			}
		}
		if e := unchangedRemoval(s, r, remote, false); e != nil {
			return e
		}
		var e error
		if remote {
			if r.attr.Dir() {
				e = s.Rmdir(r.name)
			} else {
				e = s.Remove(r.name)
			}
		} else {
			e = os.Remove(r.name)
		}
		if e != nil {
			return fmt.Errorf("%d/%d entries deleted; stopped at %s: %w", i, len(entries), safeText(r.name), e)
		}
		if p != nil {
			p.mu.Lock()
			p.phase = "Removing sources"
			if p.removing {
				p.doneFiles++
				p.phase = "Deleting"
			}
			p.name = r.name
			p.lastChange = time.Now()
			p.log(" − "+safeText(r.name), false)
			p.mu.Unlock()
		}
	}
	return nil
}
