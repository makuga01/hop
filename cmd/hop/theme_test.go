package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThemeConfiguration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "settings")
	t.Setenv("HOP_HOME", dir)
	t.Cleanup(func() { _ = applyTheme("lagoon") })
	if err := loadTheme(""); err != nil {
		t.Fatal(err)
	}
	lagoon := uiSelected
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("reading default should not create files")
	}
	if err := configure([]string{"theme", "cobalt"}); err != nil {
		t.Fatal(err)
	}
	if err := loadTheme(""); err != nil {
		t.Fatal(err)
	}
	if uiSelected == lagoon {
		t.Fatal("saved theme did not apply")
	}
	if err := loadTheme("lagoon"); err != nil {
		t.Fatal(err)
	}
	if uiSelected != lagoon {
		t.Fatal("override did not take precedence")
	}
	values, file, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := configuredTheme(values); name != "cobalt" {
		t.Fatal("override changed saved preference")
	}
	if err := os.WriteFile(file, []byte(`{"theme":"cobalt","future":{"enabled":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := configure([]string{"theme", "Afterhours"}); err != nil {
		t.Fatal(err)
	}
	values, _, _ = readConfig()
	if _, ok := values["future"]; !ok {
		t.Fatal("lost unrelated config")
	}
	if name, _ := configuredTheme(values); name != "afterhours" {
		t.Fatal(name)
	}
	before, _ := os.ReadFile(file)
	if err := configure([]string{"theme", "missing"}); err == nil {
		t.Fatal("accepted unknown theme")
	}
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("invalid theme changed config")
	}
	for _, bad := range []string{`{`, `null`, `{"theme":12}`, `{"theme":null}`, `{"theme":"missing"}`} {
		if err := os.WriteFile(file, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if err := loadTheme(""); err == nil {
			t.Fatalf("accepted %s", bad)
		}
		if err := loadTheme("lagoon"); err != nil {
			t.Fatal("explicit override should work", err)
		}
	}
}

func TestThemeOptionsAndColor(t *testing.T) {
	t.Cleanup(func() { _ = applyTheme("lagoon") })
	t.Setenv("NO_COLOR", "")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERM", "xterm-256color")
	seen := map[string]bool{}
	for _, name := range []string{"lagoon", "cobalt", "afterhours", "black"} {
		o, err := parseOptions([]string{"a.txt", "--theme", name})
		if err != nil || o.Theme != name {
			t.Fatalf("%+v %v", o, err)
		}
		if err = loadTheme(o.Theme); err != nil {
			t.Fatal(err)
		}
		if seen[uiSelected] {
			t.Fatal("themes need distinct focus colors")
		}
		seen[uiSelected] = true
		if !strings.Contains(uiPaint("focus", uiSelected), "\x1b[") {
			t.Fatal("missing color")
		}
	}
	for _, args := range [][]string{{"--theme"}, {"--theme", ""}, {"--theme", "missing"}} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	t.Setenv("NO_COLOR", "1")
	if got := uiPaint("folder", uiFolder); got != "folder" {
		t.Fatal(got)
	}
	if got := uiPaint("focus", uiSelected); got != "\x1b[7mfocus\x1b[0m" {
		t.Fatal(got)
	}
}

func TestTerminalColorFallback(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("COLORTERM", "")
	t.Setenv("HOP_COLOR_MODE", "")
	if terminalColorMode() != "256" || !strings.HasPrefix(rgbStyle(0xe7fff5, 0x063537), "38;5;") {
		t.Fatal("Apple Terminal must receive supported indexed colors")
	}
	if indexedColor(0x063537) != 23 {
		t.Fatal("teal background became gray")
	}
	t.Setenv("COLORTERM", "truecolor")
	if !strings.HasPrefix(rgbStyle(0xe7fff5, 0x063537), "38;2;") {
		t.Fatal("truecolor capability ignored")
	}
	t.Setenv("HOP_COLOR_MODE", "256")
	if terminalColorMode() != "256" {
		t.Fatal("explicit fallback ignored")
	}
	t.Setenv("HOP_COLOR_MODE", "truecolor")
	if terminalColorMode() != "truecolor" {
		t.Fatal("explicit truecolor ignored")
	}
}

func TestInsetFrameMouseAlignment(t *testing.T) {
	for _, width := range []int{40, 80, 180} {
		b := Browser{Current: "/test"}
		m := newBrowserModel(b)
		m.items = []FileItem{{Name: "first.txt", Path: "/test/first.txt", Regular: true}, {Name: "second.txt", Path: "/test/second.txt", Regular: true}}
		gutter, _ := contentGeometry(width)
		b.mouse(m, mouseEvent{button: 0, x: gutter + 7, y: 9}, width, 24)
		if len(m.marks) != 1 || m.selected()[0].Name != "second.txt" {
			t.Fatalf("inset click missed at width %d", width)
		}
		b.mouse(m, mouseEvent{button: 2, x: 1, y: 8}, width, 24)
		if len(m.marks) != 1 {
			t.Fatal("gutter click selected a file")
		}
		frame := b.render(m, width, 24)
		if strings.Contains(frame, "\x1b[J") || strings.Contains(frame, "\x1b[2J") {
			t.Fatal("redraw clears themed background")
		}
	}
}
