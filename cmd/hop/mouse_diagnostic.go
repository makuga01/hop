package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type mouseCounts struct {
	clicks, wheel, motion int
	sgr, legacy           bool
}

func (c *mouseCounts) add(packet []byte, event mouseEvent) {
	c.sgr = c.sgr || strings.HasPrefix(string(packet), "\x1b[<")
	c.legacy = c.legacy || strings.HasPrefix(string(packet), "\x1b[M")
	if event.release {
		return
	}
	switch {
	case event.button&64 != 0:
		c.wheel++
	case event.button&32 != 0:
		c.motion++
	default:
		c.clicks++
	}
}

func (c mouseCounts) text() string {
	return fmt.Sprintf("clicks=%d scroll=%d drag=%d SGR=%t legacy=%t", c.clicks, c.wheel, c.motion, c.sgr, c.legacy)
}

// This isolated screen never opens files or SSH sessions and never prints typed
// text. Compare basic reporting with drag reporting to isolate terminal behavior.
func diagnoseMouse() error {
	tty, err := openTTY()
	if err != nil {
		return err
	}
	defer tty.Close()
	old, err := stty(tty, "-g")
	if err != nil {
		return err
	}
	if _, err = stty(tty, "raw", "-echo", "min", "0", "time", "1"); err != nil {
		return err
	}
	leave := enterPickerScreen(tty, true)
	defer func() { leave(); _, _ = stty(tty, old) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	counts := [2]mouseCounts{}
	mode := 1
	names := []string{"Basic", "Drag"}
	setMode := func(n int) {
		mode = n
		reporting := mouseOn
		if n == 0 {
			reporting = "\x1b[?1000h\x1b[?1006h"
		}
		fmt.Fprint(tty, mouseOff, reporting)
	}
	deadline := time.Now().Add(45 * time.Second)
	var pending []byte
	var buffer [128]byte
	lastDraw := time.Time{}
	running := true
	for running && time.Now().Before(deadline) {
		select {
		case <-signals:
			running = false
		default:
		}
		if !running {
			break
		}
		if time.Since(lastDraw) >= time.Second {
			fmt.Fprintf(tty, "\x1b[H\x1b[2JHop mouse test (%s) - %ds left\r\n\r\nClick and scroll anywhere in this screen.\r\nPress 1 for basic reporting; 2 for click + drag reporting.\r\nPress Q to finish. No typed text is recorded.\r\n\r\nBasic: %s\r\nDrag:  %s\r\n", names[mode], int(time.Until(deadline).Seconds()), counts[0].text(), counts[1].text())
			lastDraw = time.Now()
		}
		n, e := syscall.Read(int(tty.Fd()), buffer[:])
		if e != nil && e != syscall.EINTR {
			return e
		}
		pending = append(pending, buffer[:max(0, n)]...)
		for len(pending) > 0 {
			key, used := browserKey(pending)
			if used == 0 {
				break
			}
			if event, ok := parseMouse(key); ok {
				counts[mode].add(pending[:used], event)
			}
			pending = pending[used:]
			switch key {
			case "text:1":
				setMode(0)
			case "text:2":
				setMode(1)
			case "text:q", "text:Q", "quit":
				running = false
			}
			lastDraw = time.Time{}
		}
		if len(pending) > 256 {
			pending = nil
		}
	}
	// Leave the alternate screen before emitting a small, shareable report.
	leave()
	leave = func() {}
	_, _ = stty(tty, old)
	fmt.Printf("Hop %s mouse diagnostic\nBasic: %s\nDrag:  %s\n", version, counts[0].text(), counts[1].text())
	return nil
}
