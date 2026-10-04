package main

import (
	"context"
	"errors"
	"os"
	"runtime"
	"syscall"
)

func syncStreamBatch(ctx context.Context, files []*pendingStreamFile) error {
	return syncStreamBatchDarwin(ctx, files, syncStreamData, (*os.File).Sync)
}

func syncStreamData(f *os.File) error {
	var err error
	for {
		err = syscall.Fsync(int(f.Fd()))
		if err != syscall.EINTR {
			break
		}
	}
	runtime.KeepAlive(f)
	return err
}

// First fsync every file's data and metadata into its device. Then File.Sync
// issues F_FULLFSYNC once per filesystem device, flushing the drive's write
// cache for all those files. No file is closed or published before this barrier.
// https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fsync.2.html
func syncStreamBatchDarwin(ctx context.Context, files []*pendingStreamFile, dataSync, deviceSync func(*os.File) error) error {
	devices := make([]int32, len(files))
	err := parallelFilesN(len(files), 8, func(i int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := files[i].file
		if f == nil {
			return nil
		}
		info, err := f.Stat()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("cannot identify stream destination device")
		}
		devices[i] = stat.Dev
		return dataSync(f)
	})
	if err != nil {
		return err
	}
	flushed := map[int32]bool{}
	groupable := map[int32]bool{}
	for i, p := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.file != nil && !flushed[devices[i]] {
			group, checked := groupable[devices[i]]
			if !checked {
				// Limit shared drive-cache barriers to Apple's local disk
				// filesystems. Other filesystems retain one File.Sync per file.
				var fs syscall.Statfs_t
				if syscall.Fstatfs(int(p.file.Fd()), &fs) == nil {
					var name []byte
					for _, c := range fs.Fstypename {
						if c == 0 {
							break
						}
						name = append(name, byte(c))
					}
					group = string(name) == "apfs" || string(name) == "hfs"
				}
				groupable[devices[i]] = group
			}
			// Go retains its standard fsync fallback when F_FULLFSYNC is
			// unsupported, e.g. on SMB; all files already passed fsync above.
			if err := deviceSync(p.file); err != nil {
				return err
			}
			flushed[devices[i]] = group
		}
	}
	for _, p := range files {
		if p.file != nil {
			if err := p.file.Close(); err != nil {
				return err
			}
		}
	}
	for _, p := range files {
		p.synced = true
	}
	return ctx.Err()
}
