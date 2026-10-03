package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func themeLabel() string {
	if themePreference == "auto" {
		return "auto · " + activeTheme
	}
	return activeTheme
}

// Prefer the terminal's advertised background to the desktop appearance. Never
// read terminal input for detection: that could consume the user's first keys.
func automaticTheme() string {
	if name, ok := themeFromColorFGBG(os.Getenv("COLORFGBG")); ok {
		return name
	}
	return desktopTheme(runtime.GOOS, os.Getenv("XDG_CURRENT_DESKTOP"), appearanceOutput)
}

func appearanceOutput(command string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, command, args...).Output()
	return strings.TrimSpace(string(output)), err
}

func desktopTheme(system, desktop string, output func(string, ...string) (string, error)) string {
	if system == "darwin" {
		// Read the appearance key only, with a short timeout.
		value, err := output("/usr/bin/defaults", "read", "-g", "AppleInterfaceStyle")
		if err == nil && strings.EqualFold(value, "Dark") {
			return "dark"
		}
		// defaults returns status 1 when the key is absent (the light default).
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return "light"
		}
	}
	if system == "linux" && strings.Contains(strings.ToLower(desktop), "gnome") {
		value, err := output("gsettings", "get", "org.gnome.desktop.interface", "color-scheme")
		if err == nil {
			switch strings.Trim(value, "'") {
			case "prefer-light":
				return "light"
			case "prefer-dark":
				return "dark"
			}
		}
	}
	return "dark"
}

func themeFromColorFGBG(value string) (string, bool) {
	parts := strings.Split(value, ";")
	if len(parts) < 2 {
		return "", false
	}
	index, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || index < 0 || index > 255 {
		return "", false
	}
	var r, g, b int
	if index < 16 {
		// Standard ANSI colors; custom terminal palettes can differ. An explicit
		// --theme or saved choice always overrides this best-effort inference.
		ansi := []uint32{0x000000, 0x800000, 0x008000, 0x808000, 0x000080, 0x800080, 0x008080, 0xc0c0c0,
			0x808080, 0xff0000, 0x00ff00, 0xffff00, 0x0000ff, 0xff00ff, 0x00ffff, 0xffffff}
		color := ansi[index]
		r, g, b = int(color>>16), int(color>>8&255), int(color&255)
	} else if index < 232 {
		levels := []int{0, 95, 135, 175, 215, 255}
		n := index - 16
		r, g, b = levels[n/36], levels[n/6%6], levels[n%6]
	} else {
		r = 8 + 10*(index-232)
		g, b = r, r
	}
	if 299*r+587*g+114*b > 150000 {
		return "light", true
	}
	return "dark", true
}
