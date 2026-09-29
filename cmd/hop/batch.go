package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
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
	p := CopyPlan{Destination: dest, Direction: "upload"}
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
		a, e := sourceStat(f.Path)
		if e != nil {
			return p, e
		}
		f.Dir = a.Dir()
		f.Regular = a.Regular()
		p.Roots = append(p.Roots, f)
	}
	roots := []FileItem{}
	for _, f := range p.Roots {
		nested := false
		for _, other := range p.Roots {
			if other.Dir && other.Path != f.Path && strings.HasPrefix(f.Path, other.Path+"/") {
				nested = true
				break
			}
		}
		if !nested {
			roots = append(roots, f)
		}
	}
	p.Roots = roots
	destinations := map[string]bool{}
	ancestors := map[string]bool{}
	var walk func(string, string, int) error
	walk = func(src, rel string, depth int) error {
		if depth > 128 || len(p.Files)+len(p.Directories) >= 100000 {
			return errors.New("tree exceeds 128 levels or 100,000 entries; select a smaller directory")
		}
		a, e := sourceStat(src)
		if e != nil {
			return fmt.Errorf("cannot inspect %s: %w", safeText(src), e)
		}
		if !a.Dir() && !a.Regular() {
			return fmt.Errorf("%s is a special file; these are not supported in recursive copies (nothing copied)", safeText(src))
		}
		local, remote := src, path.Join(dest, rel)
		if get {
			local, remote = filepath.Join(dest, filepath.FromSlash(rel)), src
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
		} else {
			var err error
			old, err = s.Stat(remote, false)
			if err == nil {
				exists = true
			} else if !noSuch(err) {
				return err
			}
		}
		if a.Dir() {
			var real string
			if get {
				real, e = s.Realpath(src)
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
				children, err := s.ReadDir(src)
				if err != nil {
					return fmt.Errorf("cannot read directory %s: %w (nothing copied)", safeText(src), err)
				}
				for _, child := range children {
					if err = walk(path.Join(src, child.Name), path.Join(rel, child.Name), depth+1); err != nil {
						return err
					}
				}
			} else {
				children, err := os.ReadDir(src)
				if err != nil {
					return fmt.Errorf("cannot read directory %s: %w (nothing copied)", safeText(src), err)
				}
				for _, child := range children {
					if err = walk(filepath.Join(src, child.Name()), path.Join(rel, child.Name()), depth+1); err != nil {
						return err
					}
				}
			}
			return nil
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
		if get {
			h, err := s.open(src, 1, 0)
			if err != nil {
				return fmt.Errorf("cannot read %s: %w", safeText(src), err)
			}
			if err = s.closeHandle(h); err != nil {
				return err
			}
		} else {
			f, err := os.Open(src)
			if err != nil {
				return err
			}
			f.Close()
		}
		p.Files = append(p.Files, PlannedFile{Local: local, Remote: remote, Size: a.Size, Replace: exists})
		p.Total += a.Size
		return nil
	}
	for _, root := range roots {
		name := path.Base(root.Path)
		if name == "/" || name == "." || name == ".." {
			return p, errors.New("select a named folder, not the filesystem root")
		}
		if destinations[name] {
			return p, fmt.Errorf("two marked items are named %s; unmark one to avoid a destination collision", safeText(name))
		}
		destinations[name] = true
		if e := walk(root.Path, name, 0); e != nil {
			return p, e
		}
	}
	if len(p.Files)+len(p.Directories) == 0 {
		return p, errors.New("no files or directories selected")
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
	plan, e := planBatch(s, files, dest, get, o.Overwrite)
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
	created := []PlannedDirectory{}
	dirStatus := o.activity("Creating destination folders", 0, false)
	for _, d := range plan.Directories {
		made, err := ensureDirectory(s, d, get)
		if err != nil {
			dirStatus.Stop()
			return fmt.Errorf("could not prepare destination folders; created folders may remain: %w", err)
		}
		if made {
			created = append(created, d)
			name := d.Remote
			if get {
				name = d.Local
			}
			progress.folder(name)
		}
	}
	dirStatus.Stop()
	for i, f := range plan.Files {
		if progress != nil {
			progress.beginFile(i, f, get, plan.Destination)
		} else {
			fmt.Printf("\n[%d/%d] %s\n", i+1, len(plan.Files), safeText(filepath.Base(f.Local)))
		}
		if get {
			e = getFile(s, h, f.Remote, f.Local, o)
		} else {
			e = sendFile(s, h, f.Local, f.Remote, o)
		}
		if progress != nil {
			progress.completeFile(e)
		}
		if e != nil {
			return fmt.Errorf("%d/%d files completed; stopped at %s: %w", i, len(plan.Files), safeText(filepath.Base(f.Local)), e)
		}
	}
	finish := o.activity("Finishing folder permissions", 0, false)
	for i := len(created) - 1; i >= 0; i-- {
		d := created[i]
		if get {
			e = os.Chmod(d.Local, os.FileMode(d.Mode))
		} else {
			e = s.Chmod(d.Remote, d.Mode)
		}
		if e != nil {
			finish.Stop()
			return fmt.Errorf("files copied, but could not set directory permissions: %w", e)
		}
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
