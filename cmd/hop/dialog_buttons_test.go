package main

import (
	"os"
	"strings"
	"testing"
)

func TestDialogNavigationAndCancel(t *testing.T) {
	for _, stage := range []string{"confirm", "conflict", "error"} {
		t.Run(stage, func(t *testing.T) {
			d := managerFixture()
			cancelled := false
			d.transfer = &panelTransfer{stage: stage, action: managerAction{kind: "delete"}, cancel: func() { cancelled = true }}
			buttons := d.transfer.buttons()
			if !d.transferInput("right", nil) || d.transfer.button != 1 {
				t.Fatal("right did not focus second button")
			}
			if !strings.Contains(strings.Join(d.transferLines(80, 7), ""), "> "+buttons[1]+" <") {
				t.Fatal("selection invisible")
			}
			d.transferInput("left", nil)
			if d.transfer.button != 0 {
				t.Fatal("left failed")
			}
			d.transferInput("up", nil)
			if d.transfer.button != len(buttons)-1 {
				t.Fatal("wrap failed")
			}
			d.transferInput("panel", nil)
			if d.transfer.button != 0 {
				t.Fatal("tab failed")
			}
			for i := range buttons {
				x := 3
				for j := 0; j < i; j++ {
					x += textWidth(buttons[j]) + 7
				}
				if d.transfer.buttonAt(x) != i {
					t.Fatal("incorrect click target")
				}
			}
			if stage == "error" {
				d.transfer.button = 2
			} else {
				d.transfer.button = 1
			}
			d.transferInput("enter", nil)
			if d.transfer != nil || !cancelled {
				t.Fatal("selected cancel did not cancel")
			}
		})
	}
}

func TestDialogPreview(t *testing.T) {
	output := os.Getenv("HOP_UI_PREVIEW")
	if output == "" {
		t.Skip("preview output not requested")
	}
	t.Setenv("NO_COLOR", "")
	os.Unsetenv("NO_COLOR")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("TERM", "xterm-256color")
	previous := activeTheme
	defer applyTheme(previous)
	if err := applyTheme("black"); err != nil {
		t.Fatal(err)
	}
	d := managerFixture()
	d.transfer = &panelTransfer{stage: "confirm", button: 1, action: managerAction{kind: "delete", side: 1, files: []FileItem{{Path: "/remote/reports.csv"}, {Path: "/remote/archive", Dir: true}}}, removals: make([]removalEntry, 12)}
	if err := os.WriteFile(output, []byte(d.render(110, 28)), 0600); err != nil {
		t.Fatal(err)
	}
}
