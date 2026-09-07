package main

import (
	"os"
	"os/exec"
	"reflect"
	"testing"
)

func sortingFixture() []FileItem {
	return []FileItem{{Name: "..", Path: "/", Dir: true}, {Name: "folder", Path: "/sorting/folder", Dir: true},
		{Name: "alpha.txt", Path: "/sorting/alpha.txt", Regular: true, Size: 3, Modified: 10},
		{Name: "beta.zip", Path: "/sorting/beta.zip", Regular: true, Size: 1, Modified: 20},
		{Name: "gamma.csv", Path: "/sorting/gamma.csv", Regular: true, Size: 2, Modified: 30}}
}
func TestFileSorting(t *testing.T) {
	for _, tt := range []struct {
		field, order string
		want         []string
	}{
		{"name", "asc", []string{"alpha.txt", "beta.zip", "gamma.csv"}},
		{"name", "desc", []string{"gamma.csv", "beta.zip", "alpha.txt"}},
		{"date", "", []string{"gamma.csv", "beta.zip", "alpha.txt"}},
		{"date", "asc", []string{"alpha.txt", "beta.zip", "gamma.csv"}},
		{"size", "", []string{"alpha.txt", "gamma.csv", "beta.zip"}},
		{"size", "asc", []string{"beta.zip", "gamma.csv", "alpha.txt"}},
		{"type", "", []string{"gamma.csv", "alpha.txt", "beta.zip"}},
	} {
		items := sortingFixture()
		order, e := parseSort(tt.field, tt.order)
		if e != nil {
			t.Fatal(e)
		}
		sortFiles(items, order)
		if items[0].Name != ".." || items[1].Name != "folder" {
			t.Fatal("lost folders-first ordering")
		}
		got := []string{items[2].Name, items[3].Name, items[4].Name}
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatal(tt.field, tt.order, got)
		}
	}
	m := newBrowserModel(Browser{Current: "/sorting"})
	m.items = sortingFixture()
	m.cursor = 3
	m.toggle(m.items[3])
	m.setSort("size")
	if m.visible()[m.cursor].Name != "beta.zip" || m.selected()[0].Name != "beta.zip" {
		t.Fatal("sorting moved focus or marks")
	}
	m.apply(browserListing{Target: "/new", Items: sortingFixture()})
	if m.visible()[2].Name != "alpha.txt" || m.sortOrder.Field != "size" {
		t.Fatal("sort lost during navigation")
	}
	recent := newBrowserModel(Browser{StartRecent: true, Recent: []FileItem{sortingFixture()[4], sortingFixture()[2]}})
	if recent.visible()[0].Name != "gamma.csv" {
		t.Fatal("default suggestions reordered")
	}
	recent.setSort("name")
	if recent.visible()[0].Name != "alpha.txt" {
		t.Fatal("explicit sort ignored in suggestions")
	}
}
func TestSortCLIAndHeaders(t *testing.T) {
	o, e := parseOptions([]string{"--sort", "date", "--order", "asc", "--", "report"})
	if e != nil || o.Sort.Field != "date" || o.Sort.Desc || !o.Sort.Explicit || len(o.Pos) != 1 {
		t.Fatal(o, e)
	}
	for _, args := range [][]string{{"--sort", "bad"}, {"--sort", "bad", "--", "file"}, {"--sort"}, {"--order", "bad"}} {
		if _, e := parseOptions(args); e == nil {
			t.Fatal("invalid sort accepted", args)
		}
	}
	b := Browser{Current: "/sorting"}
	m := newBrowserModel(b)
	m.items = sortingFixture()
	b.mouse(m, mouseEvent{button: 0, x: 70, y: 6}, 80, 24)
	if m.sortOrder.Field != "date" || !m.sortOrder.Desc {
		t.Fatal("date header click failed")
	}
	b.mouse(m, mouseEvent{button: 0, x: 70, y: 6}, 80, 24)
	if m.sortOrder.Desc {
		t.Fatal("repeat header click did not reverse")
	}
	b.mouse(m, mouseEvent{button: 0, x: 35, y: 5}, 40, 24)
	if !m.sorting {
		t.Fatal("narrow sort control unavailable")
	}
	b.mouse(m, mouseEvent{button: 0, x: 10, y: 10}, 40, 24)
	if m.sortOrder.Field != "type" || m.sorting {
		t.Fatal("sort menu type click failed")
	}
	if key, n := browserKey([]byte{15}); key != "sort" || n != 1 {
		t.Fatal("Ctrl-O unavailable")
	}
}
func TestSortingTerminal(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("python3 required")
	}
	out, e := exec.Command(python, "testdata/sorting_browser.py", os.Args[0]).CombinedOutput()
	if e != nil {
		t.Fatalf("sorting PTY: %v\n%s", e, out)
	}
}
func TestSortingTerminalHelper(t *testing.T) {
	if os.Getenv("HOP_SORT_TEST") != "1" {
		t.Skip("PTY helper")
	}
	result, e := (Browser{Title: "SORT_TEST", Current: "/sorting", Load: func(string) ([]FileItem, error) { return sortingFixture(), nil }}).Run()
	if e != nil {
		t.Fatal(e)
	}
	want := []string{"gamma.csv", "alpha.txt", "beta.zip"}
	if len(result.Files) != len(want) {
		t.Fatal("wrong selection", result)
	}
	for i, f := range result.Files {
		if f.Name != want[i] {
			t.Fatal("wrong sorted click target", result)
		}
	}
	println("SORT_OK")
}
