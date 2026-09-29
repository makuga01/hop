package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostEndpointsUseEffectiveSSHConfiguration(t *testing.T) {
	dir := t.TempDir()
	included := filepath.Join(dir, "hosts.conf")
	config := filepath.Join(dir, "config")
	if err := os.WriteFile(included, []byte(`Host staging
  HostName staging.example.test
  User deploy
  Port 2222
Host backup
  HostName backup.example.test
  User archivist
  Port 22
Host ipv6
  HostName 2001:db8::42
  User ops
  Port 2200
Host inherited
  HostName inherited.example.test
`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("Include \""+included+"\"\nHost *\n  User defaultuser\n  Port 2220\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		target string
		opts   []string
		want   string
	}{
		{"staging", nil, "deploy@staging.example.test:2222"},
		{"backup", nil, "archivist@backup.example.test"},
		{"ipv6", nil, "ops@[2001:db8::42]:2200"},
		{"inherited", nil, "defaultuser@inherited.example.test:2220"},
		{"analyst@staging", []string{"-p", "2201"}, "analyst@staging.example.test:2201"},
		{"staging", []string{"-l", "operator", "-p", "2202"}, "operator@staging.example.test:2202"},
	} {
		t.Run(test.want, func(t *testing.T) {
			h := Host{Target: test.target, Options: append([]string{"-F", config}, test.opts...)}
			got, err := resolveHostEndpoint(context.Background(), h)
			if err != nil || got != test.want {
				t.Fatalf("got %q, %v; want %q", got, err, test.want)
			}
		})
	}
	hosts := []Host{{Target: "staging", Options: []string{"-F", config, "-p", "2201"}}}
	resolveHostDetails(hosts, []string{"-l", "release", "-p", "2203"})
	if hosts[0].Endpoint != "release@staging.example.test:2203" {
		t.Fatal(hosts)
	}
	key := hosts[0].Key()
	encoded, err := json.Marshal(hosts[0])
	if err != nil || strings.Contains(string(encoded), "staging.example.test") {
		t.Fatalf("display metadata leaked into saved history: %s, %v", encoded, err)
	}
	hosts[0].Endpoint = "different@destination"
	if hosts[0].Key() != key {
		t.Fatal("display metadata changed the connection identity")
	}
}

func TestUnavailableHostConfigurationKeepsMachineSelectable(t *testing.T) {
	hosts := []Host{{Target: "staging", Options: []string{"-F", filepath.Join(t.TempDir(), "missing")}}}
	resolveHostDetails(hosts, nil)
	if hosts[0].Target != "staging" || hosts[0].Endpoint != "" || !strings.Contains(hostDetail(hosts[0]), "destination unavailable") {
		t.Fatal(hosts)
	}
}
