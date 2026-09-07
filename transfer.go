package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
)

func randomID() string {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func review(h Host, from, to string, size uint64, overwrite, yes, dry bool) error {
	screenPage("Review copy")
	fmt.Printf("\nMachine  %s\nFrom     %s\nTo       %s\nSize     %s\n", safeText(h.Label()), safeText(from), safeText(to), humanSize(size))
	if overwrite {
		fmt.Println("Existing destination will be replaced.")
	}
	if dry {
		fmt.Println("Dry run: no file copied.")
		return nil
	}
	if !yes {
		return confirm("Copy this file?")
	}
	return nil
}
func sendFile(s *SFTP, h Host, local, remote string, o Options) error {
	f, e := os.Open(local)
	if e != nil {
		return e
	}
	defer f.Close()
	initial, e := f.Stat()
	if e != nil {
		return e
	}
	if !initial.Mode().IsRegular() {
		return errors.New("send currently accepts one regular file; archive a directory first")
	}
	checking := o.activity("Checking upload destination", 0, false)
	defer checking.Stop()
	home, e := s.Realpath(".")
	if e != nil {
		return e
	}
	remote = expandRemote(remote, home)
	a, e := s.Stat(remote, true)
	if e != nil && !noSuch(e) {
		return e
	}
	if e == nil && a.Dir() {
		remote = path.Join(remote, filepath.Base(local))
	}
	parent, e := s.Stat(path.Dir(remote), true)
	if e != nil {
		return e
	}
	if !parent.Dir() {
		return errors.New("destination parent is not a directory")
	}
	existing, statErr := s.Stat(remote, false)
	exists := statErr == nil
	if statErr != nil && !noSuch(statErr) {
		return statErr
	}
	if exists && !existing.Regular() {
		return errors.New("destination exists and is not a regular file (symlinks are not overwritten)")
	}
	if exists && !o.Overwrite {
		return &DestinationExistsError{Path: remote}
	}
	if exists && s.ext["posix-rename@openssh.com"] != "1" {
		return errors.New("server cannot atomically replace files; choose a new name")
	}
	checking.Stop()
	if !o.SkipReview {
		if e = review(h, local, h.Target+":"+remote, uint64(initial.Size()), exists, o.Yes, o.DryRun); e != nil {
			return e
		}
	}
	if o.DryRun {
		return nil
	}
	status := o.activity("Uploading", uint64(initial.Size()), true)
	defer status.Stop()
	temp := path.Join(path.Dir(remote), ".hop-"+randomID()+".partial")
	cleanup := true
	defer func() {
		if cleanup {
			status.Phase("Cleaning up partial upload")
			if e := s.Remove(temp); e != nil && !noSuch(e) {
				status.Stop()
				o.warning(fmt.Sprintf("Partial upload may remain at %s:%s", safeText(h.Target), safeText(temp)))
			}
		}
	}()
	if e = s.Upload(f, temp, uint64(initial.Size()), uint32(initial.Mode().Perm()), status.Update); e != nil {
		return e
	}
	status.Phase("Verifying upload")
	after, e := f.Stat()
	if e != nil {
		return e
	}
	if after.Size() != initial.Size() || !after.ModTime().Equal(initial.ModTime()) {
		return errors.New("local source changed during upload; destination was not committed")
	}
	uploaded, e := s.Stat(temp, false)
	if e != nil {
		return e
	}
	if uploaded.Size != uint64(initial.Size()) {
		return errors.New("remote size mismatch; destination was not committed")
	}
	status.Phase("Finalizing upload")
	if e = s.Rename(temp, remote, exists && o.Overwrite); e != nil {
		return fmt.Errorf("could not commit upload (destination may have changed): %w", e)
	}
	cleanup = false
	status.Stop()
	if o.Progress == nil {
		fmt.Println("\n✓ Uploaded", safeText(filepath.Base(local)))
	}
	if e = rememberTransfer(h, remote, local, "send"); e != nil {
		o.warning("Could not save transfer history: " + e.Error())
	}
	return nil
}
func getFile(s *SFTP, h Host, remote, to string, o Options) error {
	checking := o.activity("Checking download", 0, false)
	defer checking.Stop()
	home, e := s.Realpath(".")
	if e != nil {
		return e
	}
	remote = expandRemote(remote, home)
	initial, e := s.Stat(remote, true)
	if e != nil {
		return e
	}
	if !initial.Regular() {
		return errors.New("get currently accepts one regular file; browse into the directory and choose a file")
	}
	if initial.Flags&1 == 0 {
		return errors.New("server did not report file size")
	}
	to = homeExpand(to)
	if info, e := os.Stat(to); e == nil && info.IsDir() {
		to = filepath.Join(to, path.Base(remote))
	}
	to, e = filepath.Abs(to)
	if e != nil {
		return e
	}
	existing, statErr := os.Lstat(to)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if exists && !existing.Mode().IsRegular() {
		return errors.New("destination exists and is not a regular file (symlinks are not overwritten)")
	}
	if exists && !o.Overwrite {
		return &DestinationExistsError{Path: to}
	}
	checking.Stop()
	if !o.SkipReview {
		if e = review(h, h.Target+":"+remote, to, initial.Size, exists, o.Yes, o.DryRun); e != nil {
			return e
		}
	}
	if o.DryRun {
		return nil
	}
	status := o.activity("Downloading", initial.Size, true)
	defer status.Stop()
	f, e := os.CreateTemp(filepath.Dir(to), ".hop-*.partial")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = s.Download(remote, f, initial.Size, status.Update); e != nil {
		return e
	}
	status.Phase("Verifying download")
	after, e := s.Stat(remote, true)
	if e != nil {
		return e
	}
	if after.Size != initial.Size || after.Mtime != initial.Mtime {
		return errors.New("remote source changed during download; local destination was not committed")
	}
	status.Phase("Saving download")
	if e = f.Chmod(os.FileMode(initial.Mode & 0777)); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if exists && o.Overwrite {
		e = os.Rename(f.Name(), to)
	} else {
		e = os.Link(f.Name(), to)
	} // no-clobber commit, including concurrent creators
	if e != nil {
		return fmt.Errorf("could not commit download: %w", e)
	}
	status.Stop()
	if o.Progress == nil {
		fmt.Println("\n✓ Downloaded", safeText(to))
	}
	if e = rememberTransfer(h, remote, to, "get"); e != nil {
		o.warning("Could not save transfer history: " + e.Error())
	}
	return nil
}
