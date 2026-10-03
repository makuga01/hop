package main

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutomaticThemePreference(t *testing.T) {
	t.Setenv("HOP_HOME", t.TempDir())
	previous := themePreference
	t.Cleanup(func() { _ = applyTheme(previous) })
	for value, want := range map[string]string{"0;15": "light", "15;0": "dark", "0;7": "light", "0;8": "dark", "0;default;231": "light", "0;232": "dark", "0;255": "light"} {
		t.Setenv("COLORFGBG", value)
		if err := loadTheme("auto"); err != nil || activeTheme != want || themePreference != "auto" {
			t.Fatalf("%s: %s/%s, %v", value, activeTheme, themePreference, err)
		}
	}
	for _, value := range []string{"", "light", "15;-1", "0;256", "0;"} {
		if _, ok := themeFromColorFGBG(value); ok {
			t.Fatal("accepted malformed background", value)
		}
	}
	t.Setenv("COLORFGBG", "0;15")
	if err := configure([]string{"theme", "dark"}); err != nil {
		t.Fatal(err)
	}
	if err := loadTheme(""); err != nil || activeTheme != "dark" {
		t.Fatal("auto replaced saved choice", err)
	}
	if err := loadTheme("light"); err != nil || activeTheme != "light" {
		t.Fatal("override ignored", err)
	}
	values, _, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := configuredTheme(values); got != "dark" {
		t.Fatal("override persisted", got)
	}
}

func TestDesktopAppearanceFallback(t *testing.T) {
	for _, tc := range []struct {
		system, desktop, output, want string
		fail                          bool
	}{
		{"darwin", "", "Dark", "dark", false},
		{"linux", "ubuntu:GNOME", "'prefer-light'", "light", false},
		{"linux", "GNOME", "'prefer-dark'", "dark", false},
		{"linux", "GNOME", "'default'", "dark", false},
		{"linux", "GNOME", "", "dark", true},
		{"darwin", "", "", "dark", true},
		{"linux", "", "", "dark", true},
	} {
		got := desktopTheme(tc.system, tc.desktop, func(string, ...string) (string, error) {
			if tc.fail {
				return "", errors.New("unavailable")
			}
			return tc.output, nil
		})
		if got != tc.want {
			t.Fatalf("%+v: %s", tc, got)
		}
	}
}

func TestNeutralThemeTextContrast(t *testing.T) {
	luminance := func(color uint32) float64 {
		var values [3]float64
		for i, shift := range []uint{16, 8, 0} {
			v := float64(color>>shift&255) / 255
			if v <= .04045 {
				values[i] = v / 12.92
			} else {
				values[i] = math.Pow((v+.055)/1.055, 2.4)
			}
		}
		return .2126*values[0] + .7152*values[1] + .0722*values[2]
	}
	for _, name := range []string{"dark", "light", "black"} {
		p := themes[name]
		for _, pair := range [][2]uint32{{p.ink, p.background}, {p.muted, p.panel}, {p.focusInk, p.focus}, {p.accent, p.panel}, {p.folder, p.background}, {p.marked, p.background}} {
			a, b := luminance(pair[0]), luminance(pair[1])
			if ratio := (math.Max(a, b) + .05) / (math.Min(a, b) + .05); ratio < 4.5 {
				t.Errorf("%s text contrast %.2f for %06x/%06x", name, ratio, pair[0], pair[1])
			}
		}
	}
}

func TestThemeDesignPreview(t *testing.T) {
	dir := os.Getenv("HOP_THEME_PREVIEW")
	if dir == "" {
		t.Skip("preview directory not requested")
	}
	t.Setenv("HOP_COLOR_MODE", "truecolor")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	os.Unsetenv("NO_COLOR")
	previous := themePreference
	t.Cleanup(func() { _ = applyTheme(previous) })
	for _, name := range []string{"dark", "light", "black"} {
		if err := applyTheme(name); err != nil {
			t.Fatal(err)
		}
		d := managerFixture()
		d.host = Host{Target: "deploy@staging.example.test"}
		for side, m := range d.panes {
			m.current = []string{"/projects/website", "/srv/website"}[side]
			m.items = nil
			for i, file := range []string{"assets", "docs", "src", "CHANGELOG.md", "README.md", "config.json", "package.json"} {
				m.items = append(m.items, FileItem{Name: file, Path: m.current + "/" + file, Dir: i < 3, Regular: i >= 3, Size: uint64(1024 * (i + 1))})
			}
		}
		d.panes[0].cursor = 4
		frame := d.render(110, 28)
		if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(frame), 0600); err != nil {
			t.Fatal(err)
		}
		d.transfer = &panelTransfer{stage: "confirm", button: 0, action: managerAction{kind: "delete", side: 1, files: []FileItem{{Path: "/srv/website/archive", Dir: true}}}, removals: make([]removalEntry, 12)}
		frame = d.render(110, 28)
		if !strings.Contains(frame, "> Delete <") {
			t.Fatal("missing dialog selection")
		}
		if err := os.WriteFile(filepath.Join(dir, name+"-dialog.ansi"), []byte(frame), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
