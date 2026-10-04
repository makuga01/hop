package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Read-only and deliberately separate from copy scanning: deletion must never
// traverse symlinks. scandir supplies each child's lstat metadata in one walk.
const removalScanPython = `import os,sys,stat,json,base64,gzip
roots=json.load(sys.stdin.buffer)
for encoded,canonical in roots:
 p=base64.b64decode(encoded,validate=True)
 if os.path.realpath(os.path.dirname(p))!=base64.b64decode(canonical,validate=True): raise RuntimeError('shell and SFTP roots differ')
out=gzip.GzipFile(fileobj=sys.stdout.buffer,mode='wb',compresslevel=1)
out.write(b'HOPRM1\n');out.flush();sys.stdout.buffer.flush()
seen=set();count=0
def walk(p,parent,name,depth,a=None):
 global count
 if p in seen: return
 if depth>128 or count>=100000: raise RuntimeError('removal selection too large')
 seen.add(p)
 if a is None: a=os.lstat(p)
 index=count;count+=1
 out.write(json.dumps([parent,base64.b64encode(name).decode(),a.st_mode,a.st_size,int(a.st_mtime)],separators=(',',':')).encode()+b'\n')
 if count%128==0: out.flush();sys.stdout.buffer.flush()
 if stat.S_ISDIR(a.st_mode):
  with os.scandir(p) as children:
   children=sorted(children,key=lambda child:child.name)
  for child in children: walk(child.path,index,child.name,depth+1,child.stat(follow_symlinks=False))
for i,(encoded,canonical) in enumerate(roots):
 p=base64.b64decode(encoded,validate=True)
 walk(p,-i-1,p,0)
out.write(('END '+str(count)+'\n').encode());out.close()
`

func (s *SFTP) scanRemovalWithHelper(parent context.Context, files []FileItem, progress *scanProgress) ([]removalEntry, error) {
	if s == nil {
		return nil, errors.New("no remote connection")
	}
	tree := false
	for _, f := range files {
		tree = tree || f.Dir
	}
	if !tree && len(files) < 32 && s.removalScanCommand == nil {
		return nil, errors.New("small selection")
	}
	if s.removalScanCommand == nil && (s.cmd == nil || s.cmd.Path != "/usr/bin/ssh" || !validTarget(s.host.Target)) {
		return nil, errors.New("removal helper unavailable")
	}
	// Keep the outermost selected directory roots, ordered before descendants.
	names := []string{}
	seen := map[string]bool{}
	for _, f := range files {
		name := path.Clean(f.Path)
		if !path.IsAbs(name) || name == "/" {
			return nil, errors.New("invalid removal root")
		}
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	sort.Strings(names)
	roots := []string{}
	known := []Attr{}
	request := [][2]string{}
	dirs := map[string]bool{}
	for _, name := range names {
		if err := parent.Err(); err != nil {
			return nil, err
		}
		nested := false
		for p := path.Dir(name); p != "/"; p = path.Dir(p) {
			if dirs[p] {
				nested = true
				break
			}
		}
		if nested {
			continue
		}
		a, err := s.Stat(name, false)
		if err != nil {
			return nil, err
		}
		canonical, err := s.Realpath(path.Dir(name))
		if err != nil {
			return nil, err
		}
		roots = append(roots, name)
		known = append(known, a)
		dirs[name] = a.Dir()
		request = append(request, [2]string{base64.StdEncoding.EncodeToString([]byte(name)), base64.StdEncoding.EncodeToString([]byte(canonical))})
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(s.externalContext(), cancel)
	defer stop()
	var cmd *exec.Cmd
	if s.removalScanCommand != nil {
		cmd = s.removalScanCommand(ctx)
	} else {
		cmd = exec.CommandContext(ctx, "/usr/bin/ssh", append(rsyncSSHArgs(s.host), "--", s.host.Target, "python3 -c "+shellQuote(removalScanPython))...)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	compressed, err := gzip.NewReader(out)
	var entries []removalEntry
	if err == nil {
		entries, err = readRemovalScan(compressed, roots, known, progress)
		compressed.Close()
	}
	if err != nil {
		cancel()
	}
	runErr := cmd.Wait()
	if err != nil {
		return nil, err
	}
	if runErr != nil {
		return nil, runErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return entries, nil
}

func readRemovalScan(input io.Reader, roots []string, known []Attr, progress *scanProgress) ([]removalEntry, error) {
	limited := &io.LimitedReader{R: input, N: 64 << 20}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	if !scanner.Scan() || scanner.Text() != "HOPRM1" {
		return nil, errors.New("invalid removal scan header")
	}
	entries := []removalEntry{}
	depths := []int{}
	seen := map[string]bool{}
	rootSeen := make([]bool, len(roots))
	ended := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if ended {
			return nil, errors.New("trailing removal scan data")
		}
		if string(line) == "END "+strconv.Itoa(len(entries)) {
			ended = true
			continue
		}
		var row []json.RawMessage
		if err := json.Unmarshal(line, &row); err != nil || len(row) != 5 {
			return nil, errors.New("invalid removal scan record")
		}
		var parent int
		var encoded string
		var mode uint32
		var size uint64
		var modified int64
		if json.Unmarshal(row[0], &parent) != nil || json.Unmarshal(row[1], &encoded) != nil || json.Unmarshal(row[2], &mode) != nil || json.Unmarshal(row[3], &size) != nil || json.Unmarshal(row[4], &modified) != nil {
			return nil, errors.New("invalid removal scan attributes")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, err
		}
		name := string(decoded)
		depth := 0
		if strings.ContainsRune(name, 0) {
			return nil, errors.New("invalid removal name")
		}
		if parent < 0 {
			index := -(parent + 1)
			if index >= len(roots) || rootSeen[index] || name != roots[index] {
				return nil, errors.New("invalid removal root")
			}
			a := known[index]
			if mode != a.Mode || size != a.Size || uint32(modified) != a.Mtime {
				return nil, errors.New("removal root changed")
			}
			rootSeen[index] = true
		} else {
			if parent >= len(entries) || !entries[parent].attr.Dir() || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
				return nil, errors.New("invalid removal child")
			}
			entries[parent].children = append(entries[parent].children, name)
			depth = depths[parent] + 1
			name = path.Join(entries[parent].name, name)
		}
		if !path.IsAbs(name) || name == "/" || seen[name] || depth > 128 || len(entries) >= 100000 {
			return nil, errors.New("invalid or oversized removal tree")
		}
		seen[name] = true
		entries = append(entries, removalEntry{name: name, attr: Attr{Mode: mode, Size: size, Mtime: uint32(modified), Flags: 13}})
		depths = append(depths, depth)
		progress.found(entries[len(entries)-1].attr.Dir())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !ended || limited.N == 0 {
		return nil, errors.New("incomplete or oversized removal scan")
	}
	for _, found := range rootSeen {
		if !found {
			return nil, errors.New("missing removal root")
		}
	}
	for i := range entries {
		sort.Strings(entries[i].children)
	}
	// Reversing a parent-before-child listing gives a safe postorder plan.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}
