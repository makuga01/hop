package main

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Tokenize shell words without evaluating expansions, substitutions, or commands.
// Operators delimit commands. Shell history is data, never executable input.
func shellWords(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	quote := rune(0)
	escaped := false
	started := false
	flush := func() {
		if started {
			out = append(out, b.String())
			b.Reset()
			started = false
		}
	}
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			started = true
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			started = true
		case ' ', '\t', '\n':
			flush()
		case ';', '|', '&', '<', '>':
			flush()
			out = append(out, string(r))
		default:
			b.WriteRune(r)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil, fmt.Errorf("incomplete shell quoting")
	}
	flush()
	return out, nil
}
func stripHistory(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, ": ") {
		if i := strings.Index(s, ";"); i > 0 {
			s = s[i+1:]
		}
	}
	return s
}
func readTail(file string, limit int64) ([]byte, error) {
	f, e := os.Open(file)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	start := st.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, e = f.Seek(start, 0); e != nil {
		return nil, e
	}
	b, e := io.ReadAll(io.LimitReader(f, limit))
	if start > 0 {
		if i := strings.IndexByte(string(b), '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return b, e
}

var connectionFlags = map[string]bool{"-p": true, "-l": true, "-i": true, "-J": true, "-F": true}
var safeSSHOptions = map[string]bool{"port": true, "user": true, "hostname": true, "identityfile": true, "identitiesonly": true, "proxyjump": true, "addressfamily": true, "compression": true, "connecttimeout": true, "serveraliveinterval": true, "serveralivecountmax": true, "canonicalizehostname": true}

func validTarget(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, " \t\r\n\x00`$;|&<>()")
}
func normalizeTarget(s string) (string, []string, bool) {
	if strings.HasPrefix(s, "ssh://") || strings.HasPrefix(s, "sftp://") {
		u, e := url.Parse(s)
		if e != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
			return "", nil, false
		}
		h := u.Hostname()
		if u.User != nil {
			if _, set := u.User.Password(); set {
				return "", nil, false
			}
			h = u.User.Username() + "@" + h
		}
		var opts []string
		if u.Port() != "" {
			opts = []string{"-p", u.Port()}
		}
		return h, opts, validTarget(h)
	}
	return s, nil, validTarget(s)
}
func parseConnection(words []string) (Host, []string, bool) {
	if len(words) < 2 {
		return Host{}, nil, false
	}
	cmd := filepath.Base(words[0])
	if cmd != "ssh" && cmd != "scp" && cmd != "sftp" {
		return Host{}, nil, false
	}
	var opts, operands []string
	for i := 1; i < len(words); i++ {
		w := words[i]
		if w == ";" || w == "|" || w == "&" || w == ">" || w == "<" {
			break
		}
		if w == "--" {
			operands = append(operands, words[i+1:]...)
			break
		}
		if strings.HasPrefix(w, "-") && w != "-" {
			key := w
			value := ""
			if len(w) > 2 {
				key = w[:2]
				value = w[2:]
			}
			if cmd != "ssh" && key == "-P" {
				key = "-p"
			} else if cmd != "ssh" && key == "-p" {
				continue
			}
			if connectionFlags[key] || key == "-o" {
				if value == "" {
					i++
					if i >= len(words) {
						return Host{}, nil, false
					}
					value = words[i]
				}
				if key == "-o" {
					if strings.TrimSpace(value) == "" {
						return Host{}, nil, false
					}
					k, _, ok := strings.Cut(value, "=")
					if !ok {
						k = strings.Fields(value)[0]
					}
					if !safeSSHOptions[strings.ToLower(k)] {
						return Host{}, nil, false
					}
				}
				if key == "-i" || key == "-F" {
					value = homeExpand(value)
					if !filepath.IsAbs(value) {
						return Host{}, nil, false
					}
				}
				if strings.ContainsAny(value, "\x00\r\n`$") {
					return Host{}, nil, false
				}
				opts = append(opts, key, value)
			} else if key == "-4" || key == "-6" {
				opts = append(opts, key)
			} else if harmlessFlags(w) { // harmless session/UI flags are not replayed
			} else {
				return Host{}, nil, false
			}
			continue
		}
		operands = append(operands, w)
		if cmd == "ssh" {
			break
		}
	}
	if len(operands) == 0 {
		return Host{}, nil, false
	}
	if cmd == "ssh" {
		target, uriopts, ok := normalizeTarget(operands[0])
		return Host{Target: target, Options: append(opts, uriopts...), Source: "SSH history"}, nil, ok
	}
	for _, operand := range operands {
		target, remote, ok := splitRemote(operand)
		if !ok && cmd == "sftp" {
			target = operand
			ok = validTarget(target)
		}
		if ok {
			target, uriopts, valid := normalizeTarget(target)
			paths := []string{}
			if remote != "" {
				paths = append(paths, remote)
			}
			return Host{Target: target, Options: append(opts, uriopts...), Source: cmd + " history"}, paths, valid
		}
	}
	return Host{}, nil, false
}
func splitRemote(s string) (string, string, bool) {
	if strings.HasPrefix(s, "sftp://") || strings.HasPrefix(s, "ssh://") {
		u, e := url.Parse(s)
		if e != nil {
			return "", "", false
		}
		t := *u
		t.Path = ""
		return t.String(), u.Path, true
	}
	i := -1
	if a := strings.Index(s, "["); a >= 0 {
		if b := strings.Index(s[a:], "]:"); b >= 0 {
			i = a + b + 1
		}
	} else {
		i = strings.Index(s, ":")
	}
	if i <= 0 {
		return "", "", false
	}
	h := s[:i]
	if strings.Contains(h, "/") {
		return "", "", false
	}
	h = strings.ReplaceAll(strings.ReplaceAll(h, "[", ""), "]", "")
	return h, s[i+1:], validTarget(h)
}

type Discovery struct {
	Hosts []Host
	Paths map[string][]string
}

func discover(s State) Discovery {
	home, _ := os.UserHomeDir()
	return discoverInputs(s, home, []string{os.Getenv("HISTFILE"), filepath.Join(home, ".zsh_history"), filepath.Join(home, ".bash_history")}, []string{filepath.Join(home, ".ssh", "config"), "/etc/ssh/ssh_config"})
}
func discoverInputs(s State, home string, files, configs []string) Discovery {
	d := Discovery{Paths: map[string][]string{}}
	seen := map[string]int{}
	add := func(h Host) {
		if index, ok := seen[h.Key()]; !ok {
			h.Order = len(d.Hosts)
			seen[h.Key()] = len(d.Hosts)
			d.Hosts = append(d.Hosts, h)
		} else if h.LastUsed > d.Hosts[index].LastUsed {
			h.Order = d.Hosts[index].Order
			d.Hosts[index] = h
		}
	}
	for _, h := range s.Hosts {
		add(h)
	}
	read := map[string]bool{}
	for _, file := range files {
		file = homeExpand(file)
		if file == "" || read[file] {
			continue
		}
		read[file] = true
		b, e := readTail(file, 2<<20)
		if e != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			words, e := shellWords(stripHistory(lines[i]))
			if e != nil {
				continue
			}
			h, paths, ok := parseConnection(words)
			if !ok {
				continue
			}
			h.LastUsed = historyTimestamp(lines[i])
			if h.LastUsed == 0 && i > 0 && strings.HasPrefix(lines[i-1], "#") {
				h.LastUsed, _ = strconv.ParseInt(strings.TrimSpace(lines[i-1][1:]), 10, 64)
			}
			add(h)
			if len(d.Paths[h.Key()]) < 20 {
				d.Paths[h.Key()] = append(d.Paths[h.Key()], paths...)
			}
		}
	}
	for _, file := range configs {
		configHosts(file, home, map[string]bool{}, 0, add)
	}
	sort.SliceStable(d.Hosts, func(i, j int) bool { return d.Hosts[i].LastUsed > d.Hosts[j].LastUsed })
	return d
}
func configHosts(file, home string, seen map[string]bool, depth int, add func(Host)) {
	if depth > 10 || len(seen) > 100 || seen[file] {
		return
	}
	seen[file] = true
	b, e := readTail(file, 1<<20)
	if e != nil {
		return
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "="); i >= 0 && !strings.ContainsAny(line[:i], " \t") {
			line = line[:i] + " " + line[i+1:]
		}
		w, e := shellWords(line)
		if e != nil || len(w) < 2 {
			continue
		}
		switch strings.ToLower(w[0]) {
		case "host":
			for _, v := range w[1:] {
				if strings.HasPrefix(v, "#") {
					break
				}
				if !strings.ContainsAny(v, "*?!") && validTarget(v) {
					add(Host{Target: v, Source: "SSH config"})
				}
			}
		case "include":
			for _, pattern := range w[1:] {
				pattern = homeExpand(pattern)
				if !filepath.IsAbs(pattern) {
					base := filepath.Join(home, ".ssh")
					if strings.HasPrefix(file, "/etc/ssh/") {
						base = "/etc/ssh"
					}
					pattern = filepath.Join(base, pattern)
				}
				matches, _ := filepath.Glob(pattern)
				for _, m := range matches {
					configHosts(m, home, seen, depth+1, add)
				}
			}
		}
	}
}
func historyTimestamp(line string) int64 {
	if !strings.HasPrefix(line, ": ") {
		return 0
	}
	a, _, _ := strings.Cut(line[2:], ":")
	n, _ := strconv.ParseInt(a, 10, 64)
	return n
}

func remoteHints(data, home string) []Candidate {
	lines := strings.Split(data, "\n")
	out := []Candidate{}
	seen := map[string]bool{}
	for i := len(lines) - 1; i >= 0 && len(out) < 24; i-- {
		w, e := shellWords(stripHistory(lines[i]))
		if e != nil || len(w) < 2 {
			continue
		}
		cmd := path.Base(w[0])
		isCD := cmd == "cd" || cmd == "pushd"
		if !isCD && !contains([]string{"ls", "cat", "tail", "head", "less", "vim", "vi", "nano", "stat", "touch"}, cmd) {
			continue
		}
		for _, v := range w[1:] {
			if strings.HasPrefix(v, "-") {
				continue
			}
			if strings.HasPrefix(v, "~/") {
				v = path.Join(home, v[2:])
			}
			if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, "$`*?[]\x00") {
				continue
			}
			v = path.Clean(v)
			if seen[v] {
				continue
			}
			seen[v] = true
			score := 65 - len(out)
			if isCD {
				score += 15
			}
			out = append(out, Candidate{Path: v, Dir: isCD, Reason: "remote history: " + cmd + " (inferred)", Score: score})
		}
	}
	return out
}
func contains(ss []string, s string) bool {
	for _, x := range ss {
		if s == x {
			return true
		}
	}
	return false
}
func harmlessFlags(s string) bool {
	for _, r := range strings.TrimPrefix(s, "-") {
		if !strings.ContainsRune("vqtTCAXxar", r) {
			return false
		}
	}
	return true
}
func rank(cs []Candidate, query string) []Candidate {
	byPath := map[string]Candidate{}
	for _, c := range cs {
		old, ok := byPath[c.Path]
		if !ok || c.Score > old.Score {
			byPath[c.Path] = c
		}
	}
	out := []Candidate{}
	for _, c := range byPath {
		if fuzzy(c.Path+" "+c.Reason, query) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Modified != out[j].Modified {
			return out[i].Modified > out[j].Modified
		}
		return out[i].Path < out[j].Path
	})
	return out
}
func fuzzy(text, q string) bool {
	t := []rune(strings.ToLower(text))
	qrs := []rune(strings.ToLower(strings.ReplaceAll(q, " ", "")))
	i := 0
	for _, r := range t {
		if i < len(qrs) && r == qrs[i] {
			i++
		}
	}
	return i == len(qrs)
}
