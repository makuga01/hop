package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFormerHopsMigration(t *testing.T) {
	t.Setenv("HOP_HOME", "")
	t.Setenv("HOPS_HOME", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	oldDataDir := dataDir
	t.Cleanup(func() { dataDir = oldDataDir })
	legacy := legacyDirectory()
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"hosts":[{"target":"legacy-server"}]}`)
	config := []byte(`{"theme":"afterhours","future":true}`)
	for name, value := range map[string][]byte{"state.json": state, "config.json": config} {
		if err := os.WriteFile(filepath.Join(legacy, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := initDataDir(); err != nil {
		t.Fatal(err)
	}
	s, err := readState()
	if err != nil || len(s.Hosts) != 1 || s.Hosts[0].Target != "legacy-server" {
		t.Fatalf("legacy history not inherited: %+v %v", s, err)
	}
	values, file, err := readConfig()
	if err != nil || file != filepath.Join(dataDir, "config.json") {
		t.Fatalf("wrong canonical config destination: %s %v", file, err)
	}
	if name, _ := configuredTheme(values); name != "afterhours" {
		t.Fatal("legacy theme not inherited")
	}
	if err := configure([]string{"theme", "cobalt"}); err != nil {
		t.Fatal(err)
	}
	if err := updateState(func(s *State) { s.Hosts = nil }); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{"state.json": state, "config.json": config} {
		got, err := os.ReadFile(filepath.Join(legacy, name))
		if err != nil || string(got) != string(value) {
			t.Fatalf("legacy %s modified: %v", name, err)
		}
	}
	s, err = readState()
	if err != nil || len(s.Hosts) != 0 {
		t.Fatal("canonical state did not take precedence", err)
	}
	values, _, err = readConfig()
	if name, _ := configuredTheme(values); err != nil || name != "cobalt" || values["future"] == nil {
		t.Fatal("canonical config did not preserve preferences", err)
	}

	// An explicit location must remain isolated, even when legacy data exists.
	t.Setenv("HOPS_HOME", t.TempDir())
	if err := initDataDir(); err != nil {
		t.Fatal(err)
	}
	s, err = readState()
	if err != nil || len(s.Hosts) != 0 {
		t.Fatal("explicit legacy override inherited unrelated state", err)
	}
	values, _, err = readConfig()
	if err != nil || len(values) != 0 {
		t.Fatal("explicit legacy override inherited unrelated config", err)
	}
	t.Setenv("HOP_HOME", t.TempDir())
	dir, err := configDirectory()
	if err != nil || dir != os.Getenv("HOP_HOME") {
		t.Fatal("HOP_HOME must take precedence", err)
	}
}
