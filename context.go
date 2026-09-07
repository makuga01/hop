package main

import (
	"context"
	"errors"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func initialCandidates(h Host, s State, d Discovery, local string, get bool) []Candidate {
	var out []Candidate
	cache := s.Caches[h.Key()]
	for _, c := range cache.Candidates {
		c.Reason = "cached · " + strings.TrimPrefix(c.Reason, "cached · ")
		c.Score = min(c.Score, 70)
		out = append(out, c)
	}
	for _, session := range s.Sessions {
		if session.Host.Key() == h.Key() && session.CWD != "" {
			score := 100
			reason := "last tracked session directory"
			if sessionAlive(session) {
				score = 120
				reason = "active tracked session directory"
			}
			out = append(out, Candidate{Path: session.CWD, Dir: true, Score: score, Reason: reason})
		}
	}
	abs, _ := filepath.Abs(local)
	localDir := filepath.Dir(abs)
	for i, t := range s.Transfers {
		if t.Host != h.Key() {
			continue
		}
		score := 85 - i/5
		reason := "previous transfer"
		if local != "" && t.LocalDir == localDir {
			score += 15
			reason = "previous transfer from this local project"
		}
		if !get && t.Name == filepath.Base(local) {
			score += 8
			reason += " · same filename"
		}
		out = append(out, Candidate{Path: path.Dir(t.Remote), Dir: true, Reason: reason, Score: score})
		if get {
			out = append(out, Candidate{Path: t.Remote, Dir: false, Reason: reason, Score: score, Modified: t.At})
		}
	}
	for _, p := range d.Paths[h.Key()] {
		if strings.HasPrefix(p, "/") {
			out = append(out, Candidate{Path: path.Dir(p), Dir: true, Reason: "local scp/sftp history (inferred)", Score: 75})
		}
	}
	return rank(out, "")
}
func refreshContext(ctx context.Context, sftp *SFTP, home string, base []Candidate, get, noHistory bool, publish func([]Candidate, string)) []Candidate {
	cs := append([]Candidate{}, base...)
	cs = append(cs, Candidate{Path: home, Dir: true, Reason: "remote home", Score: 15}, Candidate{Path: "/tmp", Dir: true, Reason: "temporary directory", Score: 5})
	stop := func() bool { return ctx.Err() != nil }
	publish(cs, "Refreshing…")
	if !noHistory {
		for _, name := range []string{".zsh_history", ".bash_history"} {
			if stop() {
				return cs
			}
			data, e := sftp.Tail(path.Join(home, name), 128<<10)
			if e != nil {
				continue
			}
			cs = append(cs, remoteHints(data, home)...)
			publish(cs, "Refreshing…")
		}
	}
	// Examine only likely locations. Never recursively crawl the remote machine.
	checked := []Candidate{}
	dirs := []Candidate{}
	seenDirs := map[string]bool{}
	for _, c := range rank(cs, "") {
		if stop() {
			return cs
		}
		if len(checked) >= 18 {
			break
		}
		a, e := sftp.Stat(c.Path, true)
		if e != nil {
			continue
		}
		c.Dir = a.Dir()
		c.Size = a.Size
		c.Modified = int64(a.Mtime)
		checked = append(checked, c)
		p := c.Path
		if !c.Dir {
			p = path.Dir(p)
		}
		if !seenDirs[p] {
			seenDirs[p] = true
			dirs = append(dirs, Candidate{Path: p, Dir: true, Reason: c.Reason, Score: c.Score - 1})
		}
	}
	cs = append(checked, dirs...)
	publish(cs, "Refreshing…")
	for i, dir := range rank(dirs, "") {
		if i >= 4 || stop() {
			break
		}
		entries, e := sftp.ReadDir(dir.Path)
		if e != nil && len(entries) == 0 {
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Attr.Mtime > entries[j].Attr.Mtime })
		count := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name, ".") {
				continue
			}
			if !entry.Attr.Dir() && !entry.Attr.Regular() {
				continue
			}
			if !get && !entry.Attr.Dir() {
				continue
			}
			score := dir.Score - 15
			reason := "in a recently used directory"
			if entry.Attr.Dir() {
				reason = "subfolder of " + dir.Path
			} else if time.Since(time.Unix(int64(entry.Attr.Mtime), 0)) < 24*time.Hour {
				score += 25
				reason = "modified recently · " + dir.Path
			}
			cs = append(cs, Candidate{Path: path.Join(dir.Path, entry.Name), Dir: entry.Attr.Dir(), Reason: reason, Score: score, Size: entry.Attr.Size, Modified: int64(entry.Attr.Mtime)})
			count++
			if count >= 60 {
				break
			}
		}
		publish(cs, "Refreshing…")
	}
	return rank(cs, "")
}
func browseRemote(sftp *SFTP, h Host, st State, d Discovery, local string, folderOnly, noHistory bool, selected []FileItem, dryRun bool, sorting SortOrder) (BrowserResult, error) {
	opening := startActivity("Opening remote home", 0, false)
	home, e := sftp.Realpath(".")
	opening.Stop()
	if e != nil {
		return BrowserResult{}, e
	}
	base := initialCandidates(h, st, d, local, !folderOnly)
	base = append(base, Candidate{Path: home, Dir: true, Reason: "home", Score: 15})
	updates := make(chan []FileItem, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	toItems := func(cs []Candidate) []FileItem {
		items := recentFiles(cs)
		if !folderOnly {
			for _, c := range rank(cs, "") {
				if !c.Dir {
					items = append(items, FileItem{Name: c.Path, Path: c.Path, Regular: true, Size: c.Size, Modified: c.Modified})
				}
			}
		}
		return items
	}
	go func() {
		defer close(done)
		publish := func(cs []Candidate, _ string) {
			items := toItems(cs)
			select {
			case updates <- items:
			default:
				select {
				case <-updates:
				default:
				}
				select {
				case updates <- items:
				default:
				}
			}
		}
		fresh := refreshContext(ctx, sftp, home, base, !folderOnly, noHistory, publish)
		_ = updateState(func(s *State) { s.Caches[h.Key()] = Cache{fresh, home, time.Now().Unix()} })
		publish(fresh, "")
	}()
	title := "Select remote files · " + h.Target
	if folderOnly {
		title = "Upload destination · " + h.Target
	}
	result, e := (Browser{Sort: sorting, Title: title, Host: h.Label(), Current: home, Home: home, FolderOnly: folderOnly, Mkdir: func(p string) error {
		if dryRun {
			return errors.New("folder creation is disabled during --dry-run")
		}
		return sftp.Mkdir(p, 0755)
	}, Load: remoteFileLoader(sftp), BeforeLoad: cancel, Recent: toItems(base), Updates: updates, StartRecent: true, Preselected: selected}).Run()
	cancel()
	if e != nil {
		sftp.Abort()
	}
	select {
	case <-done:
	default:
		busy := startActivity("Finishing lookup", 0, false)
		<-done
		busy.Stop()
	}
	return result, e
}
func pickRemote(sftp *SFTP, h Host, st State, d Discovery, local string, get, noHistory bool) (string, error) {
	result, e := browseRemote(sftp, h, st, d, local, !get, noHistory, nil, false, SortOrder{})
	if e != nil {
		return "", e
	}
	if get {
		if len(result.Files) == 0 {
			return "", errCancelled
		}
		return result.Files[0].Path, nil
	}
	return result.Directory, nil
}
func expandRemote(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return path.Join(home, p[2:])
	}
	return p
}
