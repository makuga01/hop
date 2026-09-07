package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// The interactive command owns the alternate screen; individual views borrow it.
var activeScreen *fullScreen

type fullScreen struct {
	tty           *os.File
	width, height int
	statusRow     int
	mu            sync.Mutex
}

func beginFullscreen() *fullScreen {
	if _, e := stty(os.Stdout, "-g"); e != nil {
		return nil
	}
	tty, e := openTTY()
	if e != nil {
		return nil
	}
	s := &fullScreen{tty: tty, width: 80, height: 24}
	activeScreen = s
	fmt.Fprint(tty, "\x1b[?1049h", uiPaint("\x1b[2J", uiBase))
	return s
}
func (s *fullScreen) Close() {
	if s == nil {
		return
	}
	fmt.Fprint(s.tty, mouseOff+"\x1b[r\x1b[0m\x1b[?25h\x1b[?1049l")
	activeScreen = nil
	s.tty.Close()
}
func enterPickerScreen(tty *os.File, mouse bool) func() {
	owned := activeScreen != nil
	if !owned {
		fmt.Fprint(tty, "\x1b[?1049h")
	}
	fmt.Fprint(tty, "\x1b[r\x1b[?25l", uiPaint("\x1b[2J", uiBase))
	if mouse {
		fmt.Fprint(tty, mouseOn)
	}
	return func() {
		if mouse {
			fmt.Fprint(tty, mouseOff)
		}
		fmt.Fprint(tty, "\x1b[r\x1b[0m\x1b[?25h")
		if !owned {
			fmt.Fprint(tty, "\x1b[?1049l")
		}
	}
}
func screenPage(title string) {
	if activeScreen != nil {
		activeScreen.Page(title)
	}
}
func (s *fullScreen) measure() {
	if size, e := stty(s.tty, "size"); e == nil {
		fields := strings.Fields(size)
		if len(fields) == 2 {
			if h, e := strconv.Atoi(fields[0]); e == nil && h >= 12 {
				s.height = h
			}
			if w, e := strconv.Atoi(fields[1]); e == nil && w >= 20 {
				s.width = w
			}
		}
	}
}

func (s *fullScreen) Page(title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.measure()
	_, w := contentGeometry(s.width)
	s.statusRow = 7
	nativeTop := 11
	if s.height < 18 {
		s.statusRow = 4
		nativeTop = 7
	}
	lines := make([]string, nativeTop-3)
	for i := range lines {
		lines[i] = uiPaint(strings.Repeat(" ", w), uiBase)
	}
	if s.height >= 18 {
		lines[1] = uiPaint(fitCenter("[ >_ ]", w), uiFolder)
		lines[3] = uiPaint(fitCenter(title, w), uiBase)
	}
	lines[len(lines)-1] = strings.TrimSuffix(uiRule("SSH / confirmation", w, true), "\r\n")
	fmt.Fprint(s.tty, "\x1b[r", uiPaint("\x1b[2J", uiBase), uiScreen(strings.Join(lines, "\r\n"), title, "SSH", "Ctrl-C Cancel", s.width, s.height))
	// Native OpenSSH prompts remain inside the same fullscreen surface.
	fmt.Fprintf(s.tty, "\x1b[%d;%dr\x1b[%d;1H\x1b[?25h%s", nativeTop, s.height-2, nativeTop, uiBaseSequence())
}

func (s *fullScreen) TransferPage(host, destination string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.measure()
	_, w := contentGeometry(s.width)
	lines := []string{uiSpans(w, uiBase, uiSpan{"TO  ", uiMuted}, uiSpan{host + ":" + destination, uiBase})}
	for len(lines) < 7 {
		lines = append(lines, uiPaint(strings.Repeat(" ", w), uiBase))
	}
	lines = append(lines, uiPaint(fit("FILES", w), uiMuted))
	fmt.Fprint(s.tty, "\x1b[r", uiPaint("\x1b[2J", uiBase), uiScreen(strings.Join(lines, "\r\n"), "Batch transfer", host, "Ctrl-C Cancel   One connection · SFTP", s.width, s.height))
	fmt.Fprintf(s.tty, "\x1b[11;%dr\x1b[11;1H%s", max(11, s.height-2), uiBaseSequence())
}

func fitCenter(text string, width int) string {
	return fit(strings.Repeat(" ", max(0, (width-textWidth(text))/2))+text, width)
}
func uiBaseSequence() string {
	if !colorEnabled() {
		return ""
	}
	return "\x1b[" + uiBase + "m"
}

type screenStatusWriter struct{ screen *fullScreen }

func (w screenStatusWriter) Write(p []byte) (int, error) {
	msg := strings.TrimPrefix(string(p), "\r\x1b[2K")
	if msg != "" {
		s := w.screen
		s.mu.Lock()
		defer s.mu.Unlock()
		gutter, width := contentGeometry(s.width)
		row := s.statusRow
		if row == 0 {
			row = 4
		}
		_, e := fmt.Fprintf(s.tty, "\x1b7\x1b[%d;%dH%s\x1b8", row, gutter+1, uiPaint(fitCenter(msg, width), uiMuted))
		if e != nil {
			return 0, e
		}
	}
	return len(p), nil
}
