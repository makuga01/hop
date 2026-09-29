package main

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Ask OpenSSH to evaluate aliases, Include files, wildcard defaults, and command
// line overrides. -G prints configuration without opening an SSH session.
func resolveHostEndpoint(ctx context.Context, h Host) (string, error) {
	args := append([]string{"-G"}, h.Options...)
	args = append(args, "--", h.Target)
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	cmd.WaitDelay = 100 * time.Millisecond
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			switch fields[0] {
			case "user", "hostname", "port":
				values[fields[0]] = fields[1]
			}
		}
	}
	port, err := strconv.Atoi(values["port"])
	if err != nil || port < 1 || port > 65535 || values["user"] == "" || values["hostname"] == "" {
		return "", errors.New("incomplete SSH destination")
	}
	hostname := strings.Trim(values["hostname"], "[]")
	if port != 22 {
		hostname = net.JoinHostPort(hostname, strconv.Itoa(port))
	} else if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	return values["user"] + "@" + hostname, nil
}

// Bound both concurrency and total wait so an unavailable or slow SSH config
// cannot leave the machine picker waiting indefinitely. Keep display metadata
// out of saved history and connection identity.
func resolveHostDetails(hosts []Host, overrides []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	jobs := make(chan int, len(hosts))
	for i := range hosts {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(4, len(hosts)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				hosts[i] = withOverrides(hosts[i], overrides)
				hosts[i].Endpoint = ""
				if ctx.Err() == nil {
					hosts[i].Endpoint, _ = resolveHostEndpoint(ctx, hosts[i])
				}
			}
		}()
	}
	workers.Wait()
}

func hostDetail(h Host) string {
	detail := h.Endpoint
	if detail == "" {
		detail = "destination unavailable"
	}
	if len(h.Options) > 0 {
		detail += "  " + strings.Join(h.Options, " ")
	}
	return detail
}
