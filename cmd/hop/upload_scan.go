package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os/exec"
	"path"
	"time"
)

const uploadScanPython = `import sys,os,stat,json,gzip,base64
m=json.load(gzip.GzipFile(fileobj=sys.stdin.buffer,mode='rb'))
root,canonical=[base64.b64decode(p,validate=True) for p in m['root']]
if os.path.realpath(root)!=canonical: raise RuntimeError('shell and SFTP destinations differ')
result=bytearray();missing=set()
for encoded,directory in m['paths']:
 p=base64.b64decode(encoded,validate=True)
 try:
  if os.path.dirname(p) in missing: code=0
  else:
   mode=os.lstat(p).st_mode
   code=1 if stat.S_ISDIR(mode) else 2 if stat.S_ISREG(mode) else 3
 except FileNotFoundError: code=0
 except OSError: code=4
 if directory and code==0: missing.add(p)
 result.append(code)
sys.stdout.buffer.write(b'HPSTAT1\n'+result)
`

func (s *SFTP) canCheckUploadBatch() bool {
	return s.uploadCheckCommand != nil || s.cmd != nil && s.cmd.Path == "/usr/bin/ssh" && validTarget(s.host.Target)
}

// Read-only: unknown/malformed/unavailable helpers retain the SFTP checks.
func (s *SFTP) uploadDestinationStatuses(plan CopyPlan) []byte {
	if len(plan.Files)+len(plan.Directories) < 32 {
		return nil
	}
	canonical, err := s.Realpath(plan.Destination)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(s.externalContext(), 15*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if s.uploadCheckCommand != nil {
		cmd = s.uploadCheckCommand(ctx)
	} else {
		cmd = exec.CommandContext(ctx, "/usr/bin/ssh", append(rsyncSSHArgs(s.host), "--", s.host.Target, "python3 -c "+shellQuote(uploadScanPython))...)
	}
	paths := make([][2]any, 0, len(plan.Directories)+len(plan.Files))
	for _, d := range plan.Directories {
		paths = append(paths, [2]any{base64.StdEncoding.EncodeToString([]byte(d.Remote)), true})
	}
	for _, f := range plan.Files {
		paths = append(paths, [2]any{base64.StdEncoding.EncodeToString([]byte(f.Remote)), false})
	}
	manifest, _ := json.Marshal(struct {
		Root  [2]string `json:"root"`
		Paths [][2]any  `json:"paths"`
	}{[2]string{base64.StdEncoding.EncodeToString([]byte(plan.Destination)), base64.StdEncoding.EncodeToString([]byte(canonical))}, paths})
	var packed bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&packed, gzip.BestSpeed)
	writer.Write(manifest)
	writer.Close()
	cmd.Stdin = &packed
	cmd.Stderr = io.Discard
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	if err = cmd.Start(); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(output, int64(9+len(paths))))
	if err != nil || len(data) != 8+len(paths) || string(data[:8]) != "HPSTAT1\n" {
		cancel()
		cmd.Wait()
		return nil
	}
	if err = cmd.Wait(); err != nil {
		return nil
	}
	for _, code := range data[8:] {
		if code > 4 {
			return nil
		}
	}
	return data[8:]
}

// Directory checks stay ordered even on SFTP fallback so missing subtrees can
// be skipped. File checks later use the existing bounded parallel scheduler.
func checkUploadDirectories(s *SFTP, plan CopyPlan, statuses []byte, missing map[string]bool) error {
	for i, d := range plan.Directories {
		if missing[path.Dir(d.Remote)] {
			missing[d.Remote] = true
			continue
		}
		if statuses != nil {
			switch statuses[i] {
			case 0:
				missing[d.Remote] = true
				continue
			case 1:
				continue
			}
		}
		a, err := s.Stat(d.Remote, false)
		if noSuch(err) {
			missing[d.Remote] = true
			continue
		}
		if err != nil {
			return err
		}
		if !a.Dir() {
			return &DestinationExistsError{Path: d.Remote}
		}
	}
	return nil
}
