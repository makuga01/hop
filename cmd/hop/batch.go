package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
)

type DestinationExistsError struct{ Path string }

func (e *DestinationExistsError) Error() string {
	return fmt.Sprintf("%s already exists at the destination; use --overwrite to replace", safeText(e.Path))
}

type PlannedFile struct {
	Local, Remote string
	Size          uint64
	Replace       bool
}
type PlannedDirectory struct {
	Local, Remote string
	Mode          uint32
}
type CopyPlan struct {
	remoteReadChecked      bool
	Directories            []PlannedDirectory
	Roots                  []FileItem
	Files                  []PlannedFile
	Direction, Destination string
	Total                  uint64
}

func localSources(args []string, sorting ...SortOrder) ([]FileItem, error) {
	var order SortOrder
	if len(sorting) > 0 {
		order = sorting[0]
	}
	if len(args) == 0 {
		cwd, e := os.Getwd()
		if e != nil {
			return nil, e
		}
		home, _ := os.UserHomeDir()
		r, e := (Browser{Sort: order, Title: "Select files and folders to send", Current: cwd, Home: home, Load: localFileLoader, Recent: []FileItem{{Name: cwd, Path: cwd, Dir: true}, {Name: home, Path: home, Dir: true}}}).Run()
		return r.Files, e
	}
	files := []FileItem{}
	seen := map[string]bool{}
	for _, arg := range args {
		p, e := filepath.Abs(homeExpand(arg))
		if e != nil {
			return nil, e
		}
		info, e := os.Stat(p)
		if e != nil {
			return nil, e
		}

		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil, fmt.Errorf("%s is not a regular file or directory", safeText(arg))
		}
		if !seen[p] {
			seen[p] = true
			files = append(files, FileItem{Name: info.Name(), Path: p, Dir: info.IsDir(), Regular: info.Mode().IsRegular(), Size: uint64(info.Size()), Modified: info.ModTime().Unix()})
		}
	}
	return files, nil
}

// Enumerate and validate the entire tree before creating any destination.
func planBatch(s *SFTP, files []FileItem, dest string, get, overwrite bool) (CopyPlan, error) {
	return planBatchObserved(s, files, dest, get, overwrite, nil)
}
func planBatchObserved(s *SFTP, files []FileItem, dest string, get, overwrite bool, scan *scanProgress) (CopyPlan, error) {
	scan.setPhase("Checking selection", 0)
	p, e := planBatchInternal(s, files, dest, get, overwrite, scan)
	phase := "Scan complete"
	if e != nil {
		phase = "Scan failed"
	}
	scan.setPhase(phase, 0)
	return p, e
}
func planBatchInternal(s *SFTP, files []FileItem, dest string, get, overwrite bool, scan *scanProgress) (CopyPlan, error) {
	p := CopyPlan{Destination: dest, Direction: "upload"}
	workers, err := s.planningWorkers()
	if err != nil {
		return p, err
	}
	if get {
		p.Direction = "download"
	}
	if get {
		abs, e := filepath.Abs(homeExpand(dest))
		if e != nil {
			return p, e
		}
		dest = abs
		real, e := filepath.EvalSymlinks(dest)
		if e != nil {
			return p, e
		}
		dest = real
		a, e := os.Stat(dest)
		if e != nil {
			return p, e
		}
		if !a.IsDir() {
			return p, errors.New("download destination must be a directory")
		}
	} else {
		real, e := s.Realpath(dest)
		if e != nil {
			return p, e
		}
		dest = real
		a, e := s.Stat(dest, true)
		if e != nil {
			return p, e
		}
		if !a.Dir() {
			return p, errors.New("upload destination must be a directory")
		}
	}
	p.Destination = dest
	sourceStat := func(src string) (Attr, error) {
		if get {
			return s.Stat(src, true)
		}
		a, e := os.Stat(src)
		if e != nil {
			return Attr{}, e
		}
		mode := uint32(a.Mode().Perm())
		switch {
		case a.IsDir():
			mode |= 0040000
		case a.Mode().IsRegular():
			mode |= 0100000
		case a.Mode()&os.ModeSymlink != 0:
			mode |= 0120000
		}
		return Attr{Mode: mode, Size: uint64(a.Size()), Mtime: uint32(a.ModTime().Unix()), Flags: 13}, nil
	}
	seenSource := map[string]bool{}
	for _, f := range files {
		f.Path = path.Clean(f.Path)
		if !get {
			abs, e := filepath.Abs(f.Path)
			if e != nil {
				return p, e
			}
			f.Path = abs
		}
		if seenSource[f.Path] {
			return p, fmt.Errorf("source %s selected twice", safeText(f.Path))
		}
		seenSource[f.Path] = true
		p.Roots = append(p.Roots, f)
	}
	rootAttrs := make([]Attr, len(p.Roots))
	if err := parallelFilesN(len(p.Roots), workers, func(i int) error {
		a, err := sourceStat(p.Roots[i].Path)
		if err != nil {
			return err
		}
		rootAttrs[i] = a
		p.Roots[i].Dir, p.Roots[i].Regular = a.Dir(), a.Regular()
		return nil
	}); err != nil {
		return p, err
	}
	attrsByPath := make(map[string]Attr, len(p.Roots))
	for i, root := range p.Roots {
		attrsByPath[root.Path] = rootAttrs[i]
	}
	directoryRoots := map[string]bool{}
	for _, f := range p.Roots {
		if f.Dir {
			directoryRoots[f.Path] = true
		}
	}
	roots := []FileItem{}
	for _, f := range p.Roots {
		nested := false
		for parent := path.Dir(f.Path); parent != path.Dir(parent); parent = path.Dir(parent) {
			if directoryRoots[parent] {
				nested = true
				break
			}
		}
		if !nested {
			roots = append(roots, f)
		}
	}
	p.Roots = roots
	rootNames := map[string]bool{}
	for _, root := range roots {
		name := path.Base(root.Path)
		if name == "/" || name == "." || name == ".." {
			return p, errors.New("select a named folder, not the filesystem root")
		}
		if rootNames[name] {
			return p, fmt.Errorf("two marked items are named %s; unmark one to avoid a destination collision", safeText(name))
		}
		rootNames[name] = true
	}
	scan.setPhase("Discovering files and folders", 0)
	var remoteScan remoteTreeScan
	if get {
		var err error
		remoteScan, err = s.scanWithHelper(roots, attrsByPath, scan)
		if err != nil {
			if scan != nil {
				scan.mu.Lock()
				scan.files = 0
				scan.dirs = 0
				scan.mu.Unlock()
			}
			remoteScan, err = scanRemoteTree(s, roots, attrsByPath, workers, scan)
		}
		if err != nil {
			return p, err
		}
	}
	ancestors := map[string]bool{}
	missingDirectories := map[string]bool{}
	deferUploadChecks := !get && s.canCheckUploadBatch()
	var walk func(string, string, int, *Attr) error
	walk = func(src, rel string, depth int, known *Attr) error {
		if err := s.externalContext().Err(); err != nil {
			return err
		}
		if depth > 128 || len(p.Files)+len(p.Directories) >= 100000 {
			return errors.New("tree exceeds 128 levels or 100,000 entries; select a smaller directory")
		}
		var a Attr
		var e error
		if cached, ok := remoteScan.attrs[src]; get && ok {
			a = cached
		} else if known != nil && known.Flags&5 == 5 && (known.Regular() || known.Dir()) {
			a = *known
		} else {
			a, e = sourceStat(src)
		}
		if e != nil {
			return fmt.Errorf("cannot inspect %s: %w", safeText(src), e)
		}
		if !a.Dir() && !a.Regular() {
			return fmt.Errorf("%s is a special file; these are not supported in recursive copies (nothing copied)", safeText(src))
		}
		if !get {
			scan.found(a.Dir())
		}
		local, remote := src, path.Join(dest, rel)
		if get {
			local, remote = filepath.Join(dest, filepath.FromSlash(rel)), src
		}
		if a.Dir() {
			exists := false
			old := Attr{}
			if get {
				info, err := os.Lstat(local)
				if err == nil {
					exists = true
					if info.IsDir() {
						old.Mode = 0040000
					} else if info.Mode().IsRegular() {
						old.Mode = 0100000
					}
				} else if !os.IsNotExist(err) {
					return err
				}
			} else if !deferUploadChecks && !missingDirectories[path.Dir(remote)] {
				var err error
				old, err = s.Stat(remote, false)
				if err == nil {
					exists = true
				} else if !noSuch(err) {
					return err
				}
			}
			if !get && !deferUploadChecks && !exists {
				missingDirectories[remote] = true
			}
			var real string
			if get {
				real = remoteScan.dirs[src].real
			} else {
				real, e = filepath.EvalSymlinks(src)
			}
			if e != nil {
				return e
			}
			if ancestors[real] {
				return fmt.Errorf("symlink loop at %s (nothing copied)", safeText(src))
			}
			ancestors[real] = true
			defer delete(ancestors, real)
			if exists && !old.Dir() {
				return fmt.Errorf("destination %s is not a directory", safeText(rel))
			}
			p.Directories = append(p.Directories, PlannedDirectory{Local: local, Remote: remote, Mode: a.Mode & 0777})
			if get {
				children := remoteScan.dirs[src].children
				for _, child := range children {
					if err := walk(path.Join(src, child.Name), path.Join(rel, child.Name), depth+1, &child.Attr); err != nil {
						return err
					}
				}
			} else {
				children, err := os.ReadDir(src)
				if err != nil {
					return fmt.Errorf("cannot read directory %s: %w (nothing copied)", safeText(src), err)
				}
				for _, child := range children {
					if err = walk(filepath.Join(src, child.Name()), path.Join(rel, child.Name()), depth+1, nil); err != nil {
						return err
					}
				}
			}
			return nil
		}
		p.Files = append(p.Files, PlannedFile{Local: local, Remote: remote, Size: a.Size})
		p.Total += a.Size
		return nil
	}
	for _, root := range roots {
		name := path.Base(root.Path)
		attr := attrsByPath[root.Path]
		if e := walk(root.Path, name, 0, &attr); e != nil {
			return p, e
		}
	}
	if len(p.Files)+len(p.Directories) == 0 {
		return p, errors.New("no files or directories selected")
	}
	var uploadStatuses []byte
	if deferUploadChecks {
		scan.setPhase("Checking upload destinations", len(p.Files))
		uploadStatuses = s.uploadDestinationStatuses(p)
		if err := checkUploadDirectories(s, p, uploadStatuses, missingDirectories); err != nil {
			return p, err
		}
	}
	p.remoteReadChecked = remoteScan.readChecked
	scan.setPhase("Checking destinations and read access", len(p.Files))
	// Independent file checks overlap their network round trips. Directory checks
	// above remain ordered so a symlink cannot substitute for a destination folder.
	if err := parallelFilesN(len(p.Files), workers, func(i int) error {
		f := &p.Files[i]
		local, remote := f.Local, f.Remote
		rel := remote
		if get {
			rel = local
		}
		exists := false
		old := Attr{}
		if get {
			info, err := os.Lstat(local)
			if err == nil {
				exists = true
				if info.IsDir() {
					old.Mode = 0040000
				} else if info.Mode().IsRegular() {
					old.Mode = 0100000
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		} else if uploadStatuses != nil && uploadStatuses[len(p.Directories)+i] <= 3 {
			switch uploadStatuses[len(p.Directories)+i] {
			case 1:
				exists = true
				old.Mode = 0040000
			case 2:
				exists = true
				old.Mode = 0100000
			case 3:
				exists = true
			}
		} else if !missingDirectories[path.Dir(remote)] {
			var err error
			old, err = s.Stat(remote, false)
			if err == nil {
				exists = true
			} else if !noSuch(err) {
				return err
			}
		}
		if exists && !old.Regular() {
			return fmt.Errorf("destination %s is not a regular file", safeText(rel))
		}
		if exists && !overwrite {
			return &DestinationExistsError{Path: rel}
		}
		if exists && !get && s.ext["posix-rename@openssh.com"] != "1" {
			return errors.New("server lacks atomic overwrite support")
		}
		if get && !remoteScan.readChecked {
			h, err := s.open(remote, 1, 0)
			if err != nil {
				return fmt.Errorf("cannot read %s: %w", safeText(remote), err)
			}
			if err = s.closeHandle(h); err != nil {
				return err
			}
		} else if !get {
			f, err := os.Open(local)
			if err != nil {
				return err
			}
			f.Close()
		}

		f.Replace = exists
		scan.checkedFile()
		return nil
	}); err != nil {
		return p, err
	}
	return p, nil
}

func copyBatch(s *SFTP, h Host, files []FileItem, dest string, get bool, o Options) error {
	busy := startActivity("Scanning selection and checking destinations", 0, false)
	if !get {
		home, e := s.Realpath(".")
		if e != nil {
			busy.Stop()
			return e
		}
		dest = expandRemote(dest, home)
	}
	scan := newScanProgress()
	busy.mu.Lock()
	busy.scan = scan
	busy.mu.Unlock()
	plan, e := planBatchObserved(s, files, dest, get, o.Overwrite, scan)
	busy.Stop()
	if e != nil {
		return e
	}
	screenPage("Review copy")
	fmt.Printf("\n%s %d files, %d folders · %s\nMachine  %s\nTo       %s\n\n", map[bool]string{true: "Download", false: "Upload"}[get], len(plan.Files), len(plan.Directories), humanSize(plan.Total), safeText(h.Label()), safeText(plan.Destination))
	for _, d := range plan.Directories {
		name := d.Local
		if get {
			name = d.Remote
		}
		fmt.Printf("  %s/  [directory]\n", safeText(name))
	}
	for _, f := range plan.Files {
		name := f.Local
		if get {
			name = f.Remote
		}
		suffix := ""
		if f.Replace {
			suffix = "  [replace]"
		}
		fmt.Printf("  %s  %s%s\n", safeText(name), humanSize(f.Size), suffix)
	}
	if o.DryRun {
		fmt.Println("\nDry run: no files copied.")
		return nil
	}
	if !o.Yes {
		if e = confirm(fmt.Sprintf("\nCopy %d files and %d folders to %s?", len(plan.Files), len(plan.Directories), safeText(h.Label()+" · "+plan.Destination))); e != nil {
			return e
		}
	}
	progress := startBatchProgress(activeScreen, plan, h)
	defer progress.Stop()
	return executeBatchPlan(s, h, plan, get, o, progress)
}

func executeBatchPlan(s *SFTP, h Host, plan CopyPlan, get bool, o Options, progress *batchProgress) error {
	var e error
	o.Yes = true
	o.SkipReview = true
	o.Progress = progress
	dirStatus := o.activity("Preparing folders", 0, false)
	created, err := prepareDirectories(s, plan.Directories, get, dirStatus, progress)
	dirStatus.Stop()
	if err != nil {
		return fmt.Errorf("could not prepare destination folders; created folders may remain: %w", err)
	}

	if e = copyPlannedFiles(s, h, plan, get, o, progress); e != nil {
		return e
	}
	finish := o.activity("Finishing folder permissions", 0, false)
	if e = finishDirectories(s, created, get, finish); e != nil {
		finish.Stop()
		return fmt.Errorf("files copied, but could not set directory permissions: %w", e)
	}

	finish.Stop()
	progress.finish()
	if progress == nil || !progress.embedded {
		fmt.Printf("\n✓ %d files and %d folders copied · %s\n", len(plan.Files), len(plan.Directories), humanSize(plan.Total))
	}
	return nil
}

// Existing directories merge; symlinks never substitute for a planned folder.
func ensureDirectory(s *SFTP, d PlannedDirectory, get bool) (bool, error) {
	if get {
		info, e := os.Lstat(d.Local)
		if e == nil {
			if !info.IsDir() {
				return false, fmt.Errorf("%s is no longer a directory", safeText(d.Local))
			}
			return false, nil
		}
		if !os.IsNotExist(e) {
			return false, e
		}
		if e = os.Mkdir(d.Local, 0700); e != nil {
			return false, e
		}
		return true, nil
	}
	a, e := s.Stat(d.Remote, false)
	if e == nil {
		if !a.Dir() {
			return false, fmt.Errorf("%s is no longer a directory", safeText(d.Remote))
		}
		return false, nil
	}
	if !noSuch(e) {
		return false, e
	}
	if e = s.Mkdir(d.Remote, 0700); e != nil {
		return false, e
	}
	return true, nil
}
