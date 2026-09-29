package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

var errCancelled = errors.New("cancelled")

type PickItem struct {
	Label, Detail, Value string
	Dir                  bool
}
type PickUpdate struct {
	Items  []PickItem
	Status string
}
type PickResult struct {
	Item   PickItem
	Action string
}
type Picker struct {
	HostInput      bool
	Title          string
	Caption, Badge string
	Items          []PickItem
	Status         string
	Paths          bool
	Get            bool
	Updates        <-chan PickUpdate
}

func ttyAvailable() bool {
	f, e := openTTY()
	if e != nil {
		return false
	}
	f.Close()
	return true
}
func openTTY() (*os.File, error) {
	f, e := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if e == nil {
		return f, nil
	}
	// Sandboxed launchers may provide a terminal on stdin but disallow opening
	// /dev/tty. Reuse that explicitly inherited terminal, never a redirected pipe.
	if _, check := stty(os.Stdin, "-g"); check != nil {
		return nil, e
	}
	fd, dupErr := syscall.Dup(int(os.Stdin.Fd()))
	if dupErr != nil {
		return nil, dupErr
	}
	return os.NewFile(uintptr(fd), "inherited-terminal"), nil
}
func stty(tty *os.File, args ...string) (string, error) {
	c := exec.Command("/bin/stty", args...)
	c.Stdin = tty
	b, e := c.Output()
	return strings.TrimSpace(string(b)), e
}
func clip(s string, width int) string {
	r := []rune(safeText(s))
	if width < 2 {
		return ""
	}
	if len(r) > width {
		return string(r[:width-1]) + "…"
	}
	return string(r)
}
func (p Picker) Run() (PickResult, error) {
	tty, e := openTTY()
	if e != nil {
		return PickResult{}, errors.New("interactive picker requires a terminal; specify --host and --path (or use hop hosts --json)")
	}
	defer tty.Close()
	old, e := stty(tty, "-g")
	if e != nil {
		return PickResult{}, e
	}
	if _, e = stty(tty, "raw", "-echo", "min", "0", "time", "1"); e != nil {
		return PickResult{}, e
	}
	leaveScreen := enterPickerScreen(tty, false)
	defer func() { leaveScreen(); _, _ = stty(tty, old) }()
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGWINCH)
	defer signal.Stop(sigs)
	width, height := 80, 24
	measure := func() {
		s, _ := stty(tty, "size")
		parts := strings.Fields(s)
		if len(parts) == 2 {
			if n, _ := strconv.Atoi(parts[0]); n > 0 {
				height = n
			}
			if n, _ := strconv.Atoi(parts[1]); n > 0 {
				width = n
			}
		}
	}
	measure()
	keys := make(chan []byte, 8)
	done := make(chan struct{})
	readerDone := make(chan struct{})
	// Join while the terminal is still raw with VTIME=1. Restoring canonical
	// mode first can leave this reader blocked on (and stealing) the next prompt.
	defer func() { close(done); <-readerDone }()
	go func() {
		defer close(readerDone)
		b := make([]byte, 128)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, _ := syscall.Read(int(tty.Fd()), b)
			if n > 0 {
				copyB := append([]byte{}, b[:n]...)
				select {
				case keys <- copyB:
				case <-done:
					return
				}
			}
		}
	}()
	query := ""
	selected := 0
	var matches []PickItem
	var partial []byte
	draw := func() {
		oldValue := ""
		if selected >= 0 && selected < len(matches) {
			oldValue = matches[selected].Value
		}
		matches = nil
		for _, item := range p.Items {
			if fuzzy(item.Label+" "+item.Detail, query) {
				matches = append(matches, item)
			}
		}
		if oldValue != "" {
			for i, item := range matches {
				if item.Value == oldValue {
					selected = i
					break
				}
			}
		}
		if selected >= len(matches) {
			selected = max(0, len(matches)-1)
		}
		rows := max(1, height-10)
		start := 0
		if selected >= rows {
			start = selected - rows + 1
		}
		var b strings.Builder
		_, screenWidth := contentGeometry(width)
		b.WriteString(uiLine(p.caption(), screenWidth, uiMuted))
		b.WriteString(uiRule("", screenWidth, true))
		b.WriteString(uiBoxLine(" Filter: "+query, screenWidth, uiHeader))
		b.WriteString(uiRule("", screenWidth, false))
		for row := 0; row < rows; row++ {
			i := start + row
			line := ""
			style := uiBase
			if i < len(matches) {
				item := matches[i]
				line = "  " + item.Label
				if item.Detail != "" {
					if p.HostInput {
						aliasWidth := min(24, max(10, (screenWidth-6)/3))
						line = "  " + fit(item.Label, aliasWidth) + "  " + item.Detail
					} else {
						line += "   " + item.Detail
					}
				}
				if i == selected {
					style = uiSelected
					line = "› " + strings.TrimPrefix(line, "  ")
				}
			} else if row == 0 {
				line = "  No matches. Ctrl-U clears the filter."
				if p.HostInput {
					line = "  Type an SSH destination, then Enter to connect."
				}
			}
			b.WriteString(uiBoxLine(line, screenWidth, style))
		}
		b.WriteString(uiBottom(screenWidth))
		b.WriteString(uiLine(" "+p.Status, screenWidth, uiMuted))
		help := " ↑↓ Choose   Enter Select   Ctrl-U Clear   Esc Cancel"
		if p.HostInput {
			help = " ↑↓ Choose   Enter Connect   Ctrl-L Use typed host   Esc Cancel"
		}
		if p.Paths {
			help = " ↑↓ Choose   → Browse   Enter Select   Ctrl-L Path   Esc Cancel"
		}
		fmt.Fprint(tty, uiScreen(b.String(), p.Title, p.Badge, help, width, height))
	}
	draw()
	for {
		select {
		case sig := <-sigs:
			if sig == syscall.SIGWINCH {
				measure()
				draw()
			} else {
				return PickResult{}, errCancelled
			}
		case up, ok := <-p.Updates:
			if !ok {
				p.Updates = nil
				continue
			}
			p.Items = up.Items
			p.Status = up.Status
			draw()
		case input := <-keys:
			partial = append(partial, input...)
			for len(partial) > 0 {
				b := partial[0]
				if b == 27 {
					if len(partial) == 1 { // Allow escape sequences split across terminal reads.
						select {
						case more := <-keys:
							partial = append(partial, more...)
						case <-time.After(35 * time.Millisecond):
							return PickResult{}, errCancelled
						}
					}
					if len(partial) < 3 {
						partial = nil
						continue
					}
					seq := string(partial[:3])
					partial = partial[3:]
					switch seq {
					case "\x1b[A":
						selected = max(0, selected-1)
					case "\x1b[B":
						selected = min(len(matches)-1, selected+1)
					case "\x1b[C":
						if p.Paths && selected >= 0 && selected < len(matches) && matches[selected].Dir {
							return PickResult{matches[selected], "browse"}, nil
						}
					case "\x1b[D":
						if p.Paths {
							return PickResult{Action: "parent"}, nil
						}
					}
					// Selection changes must not be undone by the stable-update logic.
					matches = nil
					draw()
					continue
				}
				partial = partial[1:]
				switch b {
				case 3, 4:
					return PickResult{}, errCancelled
				case 12:
					if p.HostInput && query != "" {
						if _, _, ok := normalizeTarget(query); ok {
							return PickResult{Item: PickItem{Value: query}, Action: "literal"}, nil
						}
					}
					if p.Paths {
						return PickResult{Action: "path"}, nil
					}
				case 13, 10:
					if p.HostInput && len(matches) == 0 && query != "" {
						if _, _, ok := normalizeTarget(query); ok {
							return PickResult{Item: PickItem{Value: query}, Action: "literal"}, nil
						}
					}
					if p.Paths && (strings.HasPrefix(query, "/") || strings.HasPrefix(query, "~/")) && len(matches) == 0 {
						return PickResult{PickItem{Value: query}, "literal"}, nil
					}
					if selected >= 0 && selected < len(matches) {
						action := "select"
						if p.Get && matches[selected].Dir {
							action = "browse"
						}
						return PickResult{matches[selected], action}, nil
					}
				case 127, 8:
					r := []rune(query)
					if len(r) > 0 {
						query = string(r[:len(r)-1])
					}
					selected = 0
					matches = nil
				case 21:
					query = ""
					selected = 0
					matches = nil
				default:
					if b >= 32 {
						partial = append([]byte{b}, partial...)
						if !utf8.FullRune(partial) {
							goto waitInput
						}
						r, n := utf8.DecodeRune(partial)
						partial = partial[n:]
						if r != utf8.RuneError {
							query += string(r)
						}
						selected = 0
						matches = nil
					}
				}
				draw()
			}
		waitInput:
		}
	}
}
func prompt(question string) (string, error) {
	f, e := openTTY()
	if e != nil {
		return "", errors.New("confirmation requires a terminal; use --yes for a reviewed command")
	}
	defer f.Close()
	fmt.Fprint(f, question)
	s, e := bufio.NewReader(f).ReadString('\n')
	return strings.TrimSpace(s), e
}
func confirm(question string) error {
	for {
		s, e := prompt(question + " [Y/n — Enter to copy] ")
		if e != nil {
			return e
		}
		accept, valid := confirmationAnswer(s)
		if !valid {
			fmt.Fprintln(os.Stderr, "Press Enter to copy, or type n and Enter to cancel.")
			continue
		}
		if !accept {
			return errCancelled
		}
		return nil
	}
}
func confirmationAnswer(s string) (accept, valid bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "y", "yes":
		return true, true
	case "n", "no":
		return false, true
	default:
		return false, false
	}
}
func candidateItems(cs []Candidate, get bool) []PickItem {
	out := []PickItem{}
	for _, c := range rank(cs, "") {
		if !get && !c.Dir {
			continue
		}
		label := c.Path
		if c.Dir && !strings.HasSuffix(label, "/") {
			label += "/"
		}
		detail := ""
		if !c.Dir {
			detail = humanSize(c.Size)
		}
		out = append(out, PickItem{label, detail, c.Path, c.Dir})
	}
	return out
}
func humanSize(n uint64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func (p Picker) caption() string {
	if p.Caption != "" {
		return p.Caption
	}
	return p.Title
}
