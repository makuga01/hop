package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type rsyncCapability struct{ binary, rsh string }

func (s *SFTP) externalContext() context.Context {
	s.externalOnce.Do(func() { s.externalCtx, s.externalCancel = context.WithCancel(context.Background()) })
	return s.externalCtx
}
func (s *SFTP) stopExternal() { s.externalContext(); s.externalCancel() }

// rsync parses -e itself: quotes are doubled, not shell-backslash escaped.
func rsyncRSH(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "''") + "'"
	}
	return strings.Join(quoted, " ")
}
func rsyncSSHArgs(h Host) []string {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "RemoteCommand=none"}
	return append(args, sshArgs(h)...)
}

var rsyncVersion = regexp.MustCompile(`rsync\s+version\s+(\d+)\.(\d+)\.(\d+)`)

func usableRsync(b []byte) bool {
	m := rsyncVersion.FindSubmatch(b)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	return major > 3 || major == 3 && minor >= 1 // protected args, progress2, fsync
}
func (s *SFTP) detectRsync(h Host) *rsyncCapability {
	s.rsyncOnce.Do(func() {
		// Local protocol fixtures and SFTP-only transports must not launch SSH probes.
		if s.cmd == nil || s.cmd.Path != "/usr/bin/ssh" || (!validTarget(h.Target) || strings.Contains(h.Target, "/")) {
			return
		}
		binary, err := exec.LookPath("rsync")
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(s.externalContext(), 6*time.Second)
		defer cancel()
		local := exec.CommandContext(ctx, binary, "--version")
		b, err := local.Output()
		if err != nil || !usableRsync(b) {
			return
		}
		args := append(rsyncSSHArgs(h), "--", h.Target, "rsync --version")
		remote := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
		b, err = remote.Output()
		if err != nil || !usableRsync(b) {
			return
		}
		s.rsyncCap = &rsyncCapability{binary, rsyncRSH(append([]string{"/usr/bin/ssh"}, rsyncSSHArgs(h)...))}
	})
	return s.rsyncCap
}
func rsyncRemote(target, p string) string {
	user, host := "", target
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		user, host = host[:at+1], host[at+1:]
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return user + host + ":" + p + "/"
}

// A flat, explicitly enumerated file list avoids shell globs, directory
// recursion and filename remapping. Other layouts retain the SFTP path.
func rsyncLayout(plan CopyPlan, get bool) (string, string, []string, bool) {
	if len(plan.Files) == 0 || len(plan.Files) > 8192 {
		return "", "", nil, false
	}
	local, remote := filepath.Dir(plan.Files[0].Local), path.Dir(plan.Files[0].Remote)
	if !filepath.IsAbs(local) || !path.IsAbs(remote) {
		return "", "", nil, false
	}
	names := make([]string, len(plan.Files))
	seen := map[string]bool{}
	for i, f := range plan.Files {
		name := filepath.Base(f.Local)
		if filepath.Dir(f.Local) != local || path.Dir(f.Remote) != remote || path.Base(f.Remote) != name || strings.ContainsAny(name, "\x00/") || name == "." || name == ".." || seen[name] {
			return "", "", nil, false
		}
		seen[name] = true
		names[i] = name
	}
	if get {
		return remote, local, names, true
	}
	return local, remote, names, true
}
func rsyncCandidate(plan CopyPlan, get bool, o Options) bool {
	if o.DryRun {
		return false
	}
	switch os.Getenv("HOP_TRANSFER_BACKEND") {
	case "sftp":
		return false
	case "rsync":
		return len(plan.Files) > 0
	}

	return false
}

type rsyncSnapshot struct {
	local  os.FileInfo
	remote Attr
}

func (s *SFTP) rsyncDestination(f PlannedFile, get, overwrite bool) (bool, error) {
	var exists, regular bool
	if get {
		a, err := os.Lstat(f.Local)
		if err == nil {
			exists, regular = true, a.Mode().IsRegular()
		} else if !os.IsNotExist(err) {
			return false, err
		}
	} else {
		a, err := s.Stat(f.Remote, false)
		if err == nil {
			exists, regular = true, a.Regular()
		} else if !noSuch(err) {
			return false, err
		}
	}
	if exists && !regular {
		return false, errors.New("destination is not a regular file")
	}
	if exists && !overwrite {
		return false, &DestinationExistsError{Path: map[bool]string{true: f.Local, false: f.Remote}[get]}
	}
	if exists && !get && s.ext["posix-rename@openssh.com"] != "1" {
		return false, errors.New("server lacks atomic overwrite support")
	}
	return exists, nil
}

func (s *SFTP) cleanRsyncStage(stage string, get bool) error {
	if get {
		return os.RemoveAll(stage)
	}
	entries, err := s.ReadDir(stage)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		// Only our flat staging files and rsync's partial files belong here.
		if entry.Attr.Dir() {
			return errors.New("unexpected staging subdirectory")
		}
		if err := s.Remove(path.Join(stage, entry.Name)); err != nil {
			return err
		}
	}
	return s.Rmdir(stage)
}

// Ask the receiving filesystem to distinguish every name before rsync can
// merge case/Unicode aliases in its staging directory. Remove placeholders
// afterward so --copy-dest can still select the real destination as its basis.
func (s *SFTP) reserveRsyncNames(stage string, names []string, get bool, workers int) error {
	if err := parallelFilesN(len(names), workers, func(i int) error {
		if get {
			f, err := os.OpenFile(filepath.Join(stage, names[i]), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			return f.Close()
		}
		h, err := s.open(path.Join(stage, names[i]), 2|8|32, 0600)
		if err != nil {
			return err
		}
		return s.closeHandle(h)
	}); err != nil {
		return fmt.Errorf("cannot reserve distinct staging filenames: %w", err)
	}
	return parallelFilesN(len(names), workers, func(i int) error {
		if get {
			return os.Remove(filepath.Join(stage, names[i]))
		}
		return s.Remove(path.Join(stage, names[i]))
	})
}

type rsyncProgress struct {
	pending []byte
	p       *batchProgress
}

func (w *rsyncProgress) Write(b []byte) (int, error) {
	for _, c := range b {
		if c != '\r' && c != '\n' {
			if len(w.pending) < 8192 {
				w.pending = append(w.pending, c)
			}
			continue
		}
		fields := strings.Fields(string(w.pending))
		w.pending = w.pending[:0]
		if len(fields) < 2 || !strings.HasSuffix(fields[1], "%") {
			continue
		}
		n, err := strconv.ParseUint(strings.ReplaceAll(fields[0], ",", ""), 10, 64)
		if err != nil {
			continue
		}
		w.p.mu.Lock()
		w.p.current = min(n, w.p.total)
		w.p.lastChange = time.Now()
		if w.p.console != nil {
			w.p.console.Update(w.p.current)
		}
		w.p.mu.Unlock()
	}
	return len(b), nil
}

func tryRsyncBatch(s *SFTP, h Host, plan CopyPlan, get bool, o Options, p *batchProgress, completed []bool) (bool, error) {
	if !rsyncCandidate(plan, get, o) {
		return false, nil
	}
	source, dest, names, ok := rsyncLayout(plan, get)
	if !ok {
		return false, nil
	}
	cap := s.detectRsync(h)
	if cap == nil {
		return false, nil
	}
	workers, err := s.fileWorkers()
	if err != nil {
		return true, err
	}
	snapshots := make([]rsyncSnapshot, len(plan.Files))
	var unsupported = errors.New("source needs SFTP handling")
	err = parallelFilesN(len(plan.Files), workers, func(i int) error {
		f := plan.Files[i]
		if get {
			a, err := s.Stat(f.Remote, false)
			if err != nil {
				return err
			}
			if !a.Regular() || a.Flags&1 == 0 {
				return unsupported
			}
			snapshots[i].remote = a
		} else {
			a, err := os.Lstat(f.Local)
			if err != nil {
				return err
			}
			if !a.Mode().IsRegular() {
				return unsupported
			}
			snapshots[i].local = a
		}
		_, err := s.rsyncDestination(f, get, o.Overwrite)
		return err
	})
	if errors.Is(err, unsupported) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	stage := ""
	if get {
		stage, err = os.MkdirTemp(dest, ".hop-rsync-")
	} else {
		stage = path.Join(dest, ".hop-rsync-"+randomID())
		err = s.Mkdir(stage, 0700)
	}
	if err != nil {
		return true, err
	}
	defer func() {
		if err := s.cleanRsyncStage(stage, get); err != nil {
			o.warning("Rsync staging files may remain at " + safeText(stage) + ": " + err.Error())
		}
	}()
	if err := s.reserveRsyncNames(stage, names, get, workers); err != nil {
		return true, err
	}
	p.mu.Lock()
	p.phase = "Transferring with rsync"
	p.name = ""
	p.lastChange = time.Now()
	p.mu.Unlock()
	var list bytes.Buffer
	for _, name := range names {
		list.WriteString(name)
		list.WriteByte(0)
	}
	args := []string{"--protect-args", "--from0", "--files-from=-", "--ignore-times", "--no-links", "--no-specials", "--no-devices", "--chmod=D700,Fu+rw", "--compress", "--fsync", "--info=progress2", "--outbuf=L", "--timeout=45", "--copy-dest=" + dest, "-e", cap.rsh, "--"}
	if get {
		args = append(args, rsyncRemote(h.Target, source), stage+"/")
	} else {
		args = append(args, source+"/", rsyncRemote(h.Target, stage))
	}
	cmd := exec.CommandContext(s.externalContext(), cap.binary, args...)
	cmd.Stdin = &list
	cmd.Stdout = &rsyncProgress{p: p}
	diagnostics := &sshDiagnostics{}
	cmd.Stderr = diagnostics
	// Cancel the rsync process and its SSH child together.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil {
		return true, fmt.Errorf("rsync transfer failed; destinations not committed: %w%s", err, diagnostics.suffix())
	}
	p.mu.Lock()
	p.phase = "Verifying rsync transfer"
	p.mu.Unlock()
	// Verify every source and staged file before committing any of them.
	err = parallelFilesN(len(plan.Files), workers, func(i int) error {
		f := plan.Files[i]
		snap := snapshots[i]
		if get {
			after, err := s.Stat(f.Remote, true)
			if err != nil {
				return err
			}
			if after.Size != snap.remote.Size || after.Mtime != snap.remote.Mtime {
				return errors.New("remote source changed during rsync transfer")
			}
			a, err := os.Lstat(filepath.Join(stage, names[i]))
			if err != nil {
				return err
			}
			if !a.Mode().IsRegular() || uint64(a.Size()) != snap.remote.Size {
				return errors.New("invalid staged download")
			}
		} else {
			after, err := os.Lstat(f.Local)
			if err != nil {
				return err
			}
			if !os.SameFile(after, snap.local) || after.Size() != snap.local.Size() || !after.ModTime().Equal(snap.local.ModTime()) {
				return errors.New("local source changed during rsync transfer")
			}
			a, err := s.Stat(path.Join(stage, names[i]), false)
			if err != nil {
				return err
			}
			if !a.Regular() || a.Size != uint64(snap.local.Size()) {
				return errors.New("invalid staged upload")
			}
		}
		return nil
	})
	if err != nil {
		return true, err
	}
	p.mu.Lock()
	p.current = plan.Total
	p.phase = "Committing rsync files"
	p.mu.Unlock()
	err = parallelFilesN(len(plan.Files), workers, func(i int) error {
		f := plan.Files[i]
		snap := snapshots[i]
		exists, err := s.rsyncDestination(f, get, o.Overwrite)
		if err != nil {
			return err
		}
		if get {
			staged := filepath.Join(stage, names[i])
			if err := os.Chmod(staged, 0600); err != nil {
				return err
			}
			file, err := os.OpenFile(staged, os.O_RDWR, 0)
			if err != nil {
				return err
			}
			err = file.Chmod(os.FileMode(snap.remote.Mode & 0777))
			if err == nil {
				err = file.Sync()
			}
			ce := file.Close()
			if err == nil {
				err = ce
			}
			if err != nil {
				return err
			}
			if exists {
				err = os.Rename(staged, f.Local)
			} else {
				err = os.Link(staged, f.Local)
			}
		} else {
			staged := path.Join(stage, names[i])
			err = s.Chmod(staged, uint32(snap.local.Mode().Perm()))
			if err == nil {
				err = s.Rename(staged, f.Remote, exists)
			}
		}
		fp := p.startFile(f, get, plan.Destination)
		fp.current = f.Size
		fp.complete(err)
		completed[i] = err == nil
		return err
	})
	if err != nil {
		p.mu.Lock()
		p.current = 0
		p.mu.Unlock()
	}
	return true, err
}

// Explicit single-file CLI paths can use the batch backend when doing so
// preserves their filename. Renamed destinations retain the SFTP path.
func rsyncSingleCandidate(o Options, _ uint64) bool {
	if o.DryRun || os.Getenv("HOP_TRANSFER_BACKEND") == "sftp" {
		return false
	}
	return os.Getenv("HOP_TRANSFER_BACKEND") == "rsync"
}
func rsyncUploadDirectory(s *SFTP, local, remote string) (string, bool) {
	home, err := s.Realpath(".")
	if err != nil {
		return "", false
	}
	remote = expandRemote(remote, home)
	a, err := s.Stat(remote, true)
	if err == nil && a.Dir() {
		return remote, true
	}
	if err != nil && !noSuch(err) {
		return "", false
	}
	if path.Base(remote) == filepath.Base(local) {
		return path.Dir(remote), true
	}
	return "", false
}
func rsyncDownloadDirectory(remote, to string) (string, bool) {
	to, err := filepath.Abs(homeExpand(to))
	if err != nil {
		return "", false
	}
	if a, err := os.Stat(to); err == nil && a.IsDir() {
		return to, true
	}
	if filepath.Base(to) == path.Base(remote) {
		return filepath.Dir(to), true
	}
	return "", false
}
