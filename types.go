package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Host struct {
	Target   string   `json:"target"`
	Options  []string `json:"options,omitempty"`
	Source   string   `json:"source,omitempty"`
	LastUsed int64    `json:"last_used,omitempty"`
	Order    int      `json:"-"`
}

func (h Host) Key() string {
	b, _ := json.Marshal([]any{h.Target, strings.Join(h.Options, "\x00")})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:12])
}
func (h Host) Label() string { return h.Target + " " + strings.Join(h.Options, " ") }

type Candidate struct {
	Path     string `json:"path"`
	Dir      bool   `json:"dir"`
	Reason   string `json:"reason"`
	Score    int    `json:"score"`
	Size     uint64 `json:"size,omitempty"`
	Modified int64  `json:"modified,omitempty"`
}
type Transfer struct {
	Host      string `json:"host"`
	Remote    string `json:"remote"`
	LocalDir  string `json:"local_dir"`
	Name      string `json:"name"`
	Direction string `json:"direction"`
	At        int64  `json:"at"`
}
type Session struct {
	ID      string `json:"id"`
	Host    Host   `json:"host"`
	CWD     string `json:"cwd"`
	Updated int64  `json:"updated"`
	PID     int    `json:"pid"`
	Active  bool   `json:"active"`
}
type Cache struct {
	Candidates []Candidate `json:"candidates"`
	Home       string      `json:"home"`
	At         int64       `json:"at"`
}
type State struct {
	Hosts     []Host           `json:"hosts"`
	Transfers []Transfer       `json:"transfers"`
	Sessions  []Session        `json:"sessions"`
	Caches    map[string]Cache `json:"caches"`
}

var dataDir string

func configDirectory() (string, error) {
	dir := os.Getenv("HOP_HOME")
	if dir == "" {
		dir = os.Getenv("HOPS_HOME")
	}
	if dir == "" {
		d, e := os.UserConfigDir()
		if e != nil {
			return "", e
		}
		dir = filepath.Join(d, "Hop")
	}
	return dir, nil
}
func initDataDir() error {
	var e error
	dataDir, e = configDirectory()
	if e != nil {
		return e
	}
	return os.MkdirAll(dataDir, 0700)
}
func readState() (State, error) {
	s := State{Caches: map[string]Cache{}}
	b, e := os.ReadFile(filepath.Join(dataDir, "state.json"))
	if os.IsNotExist(e) {
		if legacy := legacyDirectory(); legacy != "" && isDefaultDataDir(dataDir) {
			if prior, err := os.ReadFile(filepath.Join(legacy, "state.json")); err == nil {
				if err = json.Unmarshal(prior, &s); err != nil {
					return s, fmt.Errorf("invalid legacy Hops state (preserved): %w", err)
				}
			} else if !os.IsNotExist(err) {
				return s, err
			}
		}
		if s.Caches == nil {
			s.Caches = map[string]Cache{}
		}
		return s, nil
	}
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, fmt.Errorf("invalid Hop state (preserved): %w", e)
	}
	if s.Caches == nil {
		s.Caches = map[string]Cache{}
	}
	return s, nil
}
func updateState(fn func(*State)) error {
	f, e := os.OpenFile(filepath.Join(dataDir, "state.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	s, e := readState()
	if e != nil {
		return e
	}
	fn(&s)
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	t, e := os.CreateTemp(dataDir, ".state-*")
	if e != nil {
		return e
	}
	defer os.Remove(t.Name())
	if _, e = t.Write(b); e == nil {
		e = t.Sync()
	}
	ce := t.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(t.Name(), filepath.Join(dataDir, "state.json"))
}
func rememberHost(h Host) error {
	h.LastUsed = time.Now().Unix()
	h.Source = "used with Hop"
	return updateState(func(s *State) {
		out := []Host{h}
		for _, old := range s.Hosts {
			if old.Key() != h.Key() {
				out = append(out, old)
			}
		}
		if len(out) > 100 {
			out = out[:100]
		}
		s.Hosts = out
	})
}
func rememberTransfer(h Host, remote, local, direction string) error {
	abs, _ := filepath.Abs(local)
	t := Transfer{h.Key(), remote, filepath.Dir(abs), filepath.Base(abs), direction, time.Now().Unix()}
	return updateState(func(s *State) {
		s.Transfers = append([]Transfer{t}, s.Transfers...)
		if len(s.Transfers) > 200 {
			s.Transfers = s.Transfers[:200]
		}
	})
}
func sessionAlive(s Session) bool {
	if !s.Active || s.PID <= 0 || syscall.Kill(s.PID, 0) != nil {
		return false
	}
	if s.ID == "" || strings.ContainsAny(s.ID, "/\\.") {
		return false
	}
	f, e := os.OpenFile(filepath.Join(dataDir, "session-"+s.ID+".lock"), os.O_RDWR, 0600)
	if e != nil {
		return false
	}
	defer f.Close()
	e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if e == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return e == syscall.EWOULDBLOCK
}
func safeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 32 || r == 127 || r >= 0x80 && r < 0xa0 || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			fmt.Fprintf(&b, "\\u%04x", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func homeExpand(s string) string {
	h, _ := os.UserHomeDir()
	if s == "~" {
		return h
	}
	if strings.HasPrefix(s, "~/") {
		return filepath.Join(h, s[2:])
	}
	if strings.HasPrefix(s, "$HOME/") {
		return filepath.Join(h, s[6:])
	}
	return s
}

// Explicit data locations are always isolated; the old app is read only as a default fallback.
func legacyDirectory() string {
	if os.Getenv("HOP_HOME") != "" || os.Getenv("HOPS_HOME") != "" {
		return ""
	}
	root, e := os.UserConfigDir()
	if e != nil {
		return ""
	}
	return filepath.Join(root, "Hops")
}
func isDefaultDataDir(dir string) bool {
	root, e := os.UserConfigDir()
	return e == nil && filepath.Clean(dir) == filepath.Join(root, "Hop")
}
