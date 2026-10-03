package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path"
	"strings"
	"time"
)

// The helper only reads metadata and opens regular files read-only. No content
// is read, and nothing is installed. Byte paths preserve non-UTF-8 filenames.
const scanPython = `import os,sys,json,stat,base64,gzip,io
enc=lambda p:base64.b64encode(p).decode('ascii')
roots=[base64.b64decode(p,validate=True) for p in json.load(sys.stdin)]
sys.stdout=io.TextIOWrapper(gzip.GzipFile(fileobj=sys.stdout.buffer,mode="wb",compresslevel=1),encoding="ascii")
count=0
listings={}
readable=set()
def visit(p,ancestors,depth,a=None,real=None,parent=None,name=None):
 global count
 count+=1
 if count>100000 or depth>128: raise RuntimeError('tree limit')
 index=count-1
 explicit_real=real is None
 if a is None: a=os.stat(p)
 if stat.S_ISDIR(a.st_mode):
  if real is None: real=os.path.realpath(p)
  if real in ancestors: raise RuntimeError('symlink loop')
  if real not in listings:
   with os.scandir(p) as it: listings[real]=sorted(it,key=lambda e:e.name)
  children=listings[real]
 elif stat.S_ISREG(a.st_mode):
  real=b''
  identity=(a.st_dev,a.st_ino,a.st_mode,a.st_size,a.st_mtime_ns,a.st_ctime_ns)
  if identity not in readable:
   fd=os.open(p,os.O_RDONLY|os.O_NONBLOCK)
   try:
    opened=os.fstat(fd)
    if not stat.S_ISREG(opened.st_mode) or (opened.st_dev,opened.st_ino)!=(a.st_dev,a.st_ino): raise RuntimeError('file changed')
   finally: os.close(fd)
   readable.add(identity)
 else: raise RuntimeError('special file')
 row={'p':enc(p if parent is None else name),'m':a.st_mode,'s':a.st_size,'t':int(a.st_mtime)}
 if parent is not None: row['i']=parent
 if real and explicit_real: row['r']=enc(real)
 print(json.dumps(row,separators=(',',':')))
 if count%256==0: sys.stdout.flush()
 if real:
  ancestors.add(real)
  try:
   for entry in children:
    child_real=None if entry.is_symlink() else os.path.join(real,entry.name)
    visit(os.path.join(p,entry.name),ancestors,depth+1,entry.stat(),child_real,index,entry.name)
  finally: ancestors.remove(real)
for root in roots: visit(root,set(),0)
print('{"done":true}')
sys.stdout.close()
`

type scanRecord struct {
	Parent *int   `json:"i,omitempty"`
	Path   string `json:"p"`
	Real   string `json:"r"`
	Mode   uint32 `json:"m"`
	Size   uint64 `json:"s"`
	Mtime  int64  `json:"t"`
	Done   bool   `json:"done"`
}

func readScanManifest(in io.Reader, roots []FileItem, known map[string]Attr, progress *scanProgress) (remoteTreeScan, error) {
	result := remoteTreeScan{attrs: map[string]Attr{}, dirs: map[string]remoteDirectoryScan{}}
	rootSet := map[string]bool{}
	for _, r := range roots {
		rootSet[r.Path] = true
	}
	reader := bufio.NewScanner(io.LimitReader(in, 64<<20))
	reader.Buffer(make([]byte, 4096), 1<<20)
	depths := map[string]int{}
	names := []string{}
	done := false
	for reader.Scan() {
		var row scanRecord
		if err := json.Unmarshal(reader.Bytes(), &row); err != nil {
			return result, err
		}
		if done {
			return result, errors.New("extra scan records")
		}
		if row.Done {
			done = true
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(row.Path)
		if err != nil {
			return result, err
		}
		name := string(raw)
		if row.Parent != nil {
			index := *row.Parent
			if index < 0 || index >= len(names) || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
				return result, errors.New("invalid scan parent or name")
			}
			parent := names[index]
			if _, ok := result.dirs[parent]; !ok {
				return result, errors.New("scan parent is not a directory")
			}
			name = path.Join(parent, name)
		}
		if !path.IsAbs(name) || path.Clean(name) != name || strings.ContainsRune(name, 0) {
			return result, errors.New("invalid scan path")
		}
		if _, ok := result.attrs[name]; ok || len(result.attrs) >= 100000 {
			return result, errors.New("duplicate or excessive scan entries")
		}
		a := Attr{Mode: row.Mode, Size: row.Size, Mtime: uint32(row.Mtime), Flags: 13}
		if !a.Dir() && !a.Regular() {
			return result, errors.New("special scan entry")
		}
		if rootSet[name] {
			expected := known[name]
			if a.Mode != expected.Mode || a.Size != expected.Size || a.Mtime != expected.Mtime {
				return result, errors.New("shell and SFTP metadata differ")
			}
		} else {
			parent := path.Dir(name)
			d, ok := result.dirs[parent]
			if !ok {
				return result, errors.New("scan entry outside selected tree")
			}
			depths[name] = depths[parent] + 1
			if depths[name] > 128 {
				return result, errors.New("scan depth exceeded")
			}
			d.children = append(d.children, Entry{Name: path.Base(name), Attr: a})
			result.dirs[parent] = d
		}
		if a.Dir() {
			raw, err = base64.StdEncoding.DecodeString(row.Real)
			if err != nil {
				return result, err
			}
			real := string(raw)
			if real == "" && row.Parent != nil {
				real = path.Join(result.dirs[names[*row.Parent]].real, path.Base(name))
			}
			if !path.IsAbs(real) || path.Clean(real) != real || strings.ContainsRune(real, 0) {
				return result, errors.New("invalid canonical path")
			}
			for parent := path.Dir(name); !rootSet[name] && parent != path.Dir(parent); parent = path.Dir(parent) {
				if d, ok := result.dirs[parent]; ok && d.real == real {
					return result, errors.New("symlink loop")
				}
				if rootSet[parent] {
					break
				}
			}
			result.dirs[name] = remoteDirectoryScan{real: real}
		}
		result.attrs[name] = a
		names = append(names, name)
		progress.found(a.Dir())
	}
	if err := reader.Err(); err != nil {
		return result, err
	}
	if !done {
		return result, errors.New("incomplete scan")
	}
	for root := range rootSet {
		if _, ok := result.attrs[root]; !ok {
			return result, errors.New("missing scan root")
		}
	}
	result.readChecked = true
	return result, nil
}

func (s *SFTP) scanWithHelper(roots []FileItem, known map[string]Attr, progress *scanProgress) (remoteTreeScan, error) {
	// A separate SSH session costs more than a few direct file checks.
	tree := false
	for _, root := range roots {
		tree = tree || root.Dir
	}
	if !tree && len(roots) < 64 {
		return remoteTreeScan{}, errors.New("small file selection")
	}
	ctx, cancel := context.WithTimeout(s.externalContext(), 15*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if s.scanCommand != nil {
		cmd = s.scanCommand(ctx)
	} else {
		h := s.host
		if s.cmd == nil || s.cmd.Path != "/usr/bin/ssh" || !validTarget(h.Target) {
			return remoteTreeScan{}, errors.New("scan helper unavailable")
		}
		cmd = exec.CommandContext(ctx, "/usr/bin/ssh", append(rsyncSSHArgs(h), "--", h.Target, "python3 -c "+shellQuote(scanPython))...)
	}
	names := make([]string, len(roots))
	for i, r := range roots {
		names[i] = base64.StdEncoding.EncodeToString([]byte(r.Path))
	}
	input, err := json.Marshal(names)
	if err != nil {
		return remoteTreeScan{}, err
	}
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		return remoteTreeScan{}, err
	}
	if err = cmd.Start(); err != nil {
		return remoteTreeScan{}, err
	}
	compressed, parseErr := gzip.NewReader(out)
	var result remoteTreeScan
	if parseErr == nil {
		result, parseErr = readScanManifest(compressed, roots, known, progress)
		compressed.Close()
	}
	if parseErr != nil {
		cancel()
	}
	runErr := cmd.Wait()
	if parseErr != nil {
		return result, parseErr
	}
	if runErr != nil {
		return result, runErr
	}
	// A shell can expose a different namespace than an SFTP-only/chroot account.
	for _, r := range roots {
		if r.Dir {
			real, err := s.Realpath(r.Path)
			if err != nil {
				return result, err
			}
			if real != result.dirs[r.Path].real {
				return result, errors.New("shell and SFTP roots differ")
			}
		}
	}
	return result, nil
}
