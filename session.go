package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Shell quoting is confined to our explicit tracked-session bootstrap. File
// transfer and history discovery never construct executable shell commands.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func trackingCommand(nonce string) string {
	// Bash reads this rc script through an inherited descriptor. Nothing is
	// installed or written remotely. The user's usual .bashrc runs first.
	rc := `[[ -f ~/.bashrc ]] && source ~/.bashrc
__hop_report_pwd() {
  local __hop_status=$?
  printf '\033]777;hop;NONCE;'
  printf '%s' "$PWD" | base64 | tr -d '\r\n'
  printf '\007'
  return "$__hop_status"
}
if declare -p PROMPT_COMMAND 2>/dev/null | command grep -q 'declare -a'; then
  PROMPT_COMMAND=(__hop_report_pwd "${PROMPT_COMMAND[@]}")
else
  PROMPT_COMMAND="__hop_report_pwd${PROMPT_COMMAND:+; $PROMPT_COMMAND}"
fi
`
	rc = strings.ReplaceAll(rc, "NONCE", nonce)
	bootstrap := "exec bash --noprofile --rcfile /dev/fd/3 -i 3<<'HOP_RC_" + nonce + "'\n" + rc + "HOP_RC_" + nonce + "\n"
	return "bash -c " + shellQuote(bootstrap)
}

type trackingWriter struct {
	out     io.Writer
	prefix  []byte
	pending []byte
	onPath  func(string)
}

func (w *trackingWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.pending = append(w.pending, p...)
	for len(w.pending) > 0 {
		i := bytes.Index(w.pending, w.prefix)
		if i < 0 {
			keep := min(len(w.pending), len(w.prefix)-1)
			for keep > 0 && !bytes.Equal(w.pending[len(w.pending)-keep:], w.prefix[:keep]) {
				keep--
			}
			cut := len(w.pending) - keep
			if cut > 0 {
				if _, e := w.out.Write(w.pending[:cut]); e != nil {
					return 0, e
				}
				w.pending = w.pending[cut:]
			}
			break
		}
		if i > 0 {
			if _, e := w.out.Write(w.pending[:i]); e != nil {
				return 0, e
			}
			w.pending = w.pending[i:]
		}
		end := bytes.IndexByte(w.pending[len(w.prefix):], 7)
		if end < 0 {
			if len(w.pending) > 32768 {
				_, _ = w.out.Write(w.pending)
				w.pending = nil
			}
			break
		}
		end += len(w.prefix)
		encoded := string(w.pending[len(w.prefix):end])
		decoded, e := base64.StdEncoding.DecodeString(encoded)
		if e == nil && len(decoded) <= 8192 && len(decoded) > 0 && decoded[0] == '/' && !bytes.ContainsRune(decoded, 0) {
			w.onPath(string(decoded))
		}
		w.pending = w.pending[end+1:]
	}
	return n, nil
}
func (w *trackingWriter) Flush() { _, _ = w.out.Write(w.pending); w.pending = nil }
func runSSH(h Host, track bool) error {
	if !track {
		cmd := exec.Command("/usr/bin/ssh", append(sshArgs(h), "--", h.Target)...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = rememberHost(h)
		return cmd.Run()
	}
	if !ttyAvailable() {
		return fmt.Errorf("tracked SSH needs an interactive terminal")
	}
	fmt.Println("Hop tracking: starting remote Bash with a temporary directory-reporting hook.")
	session := Session{ID: randomID(), Host: h, Updated: time.Now().Unix(), PID: os.Getpid(), Active: true}
	lock, e := os.OpenFile(filepath.Join(dataDir, "session-"+session.ID+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	defer os.Remove(lock.Name())
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return e
	}
	save := func() {
		if e := updateState(func(s *State) {
			found := false
			for i := range s.Sessions {
				if s.Sessions[i].ID == session.ID {
					s.Sessions[i] = session
					found = true
					break
				}
			}
			if !found {
				s.Sessions = append([]Session{session}, s.Sessions...)
			}
			if len(s.Sessions) > 50 {
				s.Sessions = s.Sessions[:50]
			}
		}); e != nil {
			fmt.Fprintln(os.Stderr, "Hop could not save session:", e)
		}
	}
	save()
	defer func() { session.Active = false; save() }()
	_ = rememberHost(h)
	nonce := randomID()
	writer := &trackingWriter{out: os.Stdout, prefix: []byte("\x1b]777;hop;" + nonce + ";"), onPath: func(p string) { session.CWD = p; session.Updated = time.Now().Unix(); save() }}
	defer writer.Flush()
	args := append(sshArgs(h), "-tt", "-o", "RemoteCommand=none", "--", h.Target, trackingCommand(nonce))
	cmd := exec.Command("/usr/bin/ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = writer
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
