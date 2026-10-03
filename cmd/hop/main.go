package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

const help = `Hop — SSH-only, two-panel file transfer.

  hop                         Pick a recent SSH machine and browse both sides
  hop my-server               Connect to an SSH alias or user@address
  hop --last                  Connect to the most recent machine
  hop my-server --to ~/Projects --path /srv/app
  hop demo                    Try both panels offline; copying is disabled
  hop hosts                   List discovered machines
  hop config theme dark       Save the default dark theme
  hop doctor                  Check local setup
  hop doctor mouse            Diagnose terminal mouse input

Options:
  --host HOST    --last         Choose a machine
  --to PATH                    Starting local directory (default .)
  --path PATH                  Starting remote directory (default remote home)
  --theme NAME                 Theme override for this run
  --sort name|date|size|type    Initial sort for both panels
  --order asc|desc             Initial direction
  --overwrite                  Allow replacing existing destination files
  --yes                        Skip copy review
  --dry-run                    Preview transfers; disable folder creation
  --no-history                 Do not read remote history for recent locations
  -p PORT  -i KEY  -J JUMP  -F CONFIG    OpenSSH connection overrides

Tab switches panels. ↑↓ or wheel moves the cursor. Space marks/unmarks without
moving it. Enter opens a directory or marks a file; Backspace goes to the parent.
c / Ctrl-S copies to the opposite panel; m moves after confirmation.
dd or D deletes selected files/folders on the active side after confirmation.
Ctrl-B changes the SSH machine. Ctrl-N creates a folder in the active panel.
Ctrl-L enters a path. Ctrl-R picks recent paths. Ctrl-E refreshes both panels.
Ctrl-O cycles name/date/size/type sorting; click column headers to reverse order.
/ focuses the filter; Esc/Enter leaves it. Ctrl-U clears. q / Ctrl-C quits.
Esc in normal mode clears all marks in the active panel before filter/status.
j/k move down/up; l opens; H/u goes to parent; gg/G goes first/last.
o opens session/connection Options. Conflicts offer Replace without restarting.
h or ? shows all commands. Normal letters never modify the filter.
Clicking the inactive panel only focuses it. On the active panel, single click
moves the cursor, double-click opens a folder, and right click marks items.
The wheel always moves the active panel cursor, wherever the pointer is.
Click/drag the scrollbar. Marked folders include all their contents.

Transfers use SFTP over your system's OpenSSH. Nothing is installed remotely.
Settings/history use the OS user config directory under Hop (HOP_HOME overrides).
The former Hops app's settings/history are read when the new location is empty.
The inherited send/get/ssh and shell-init commands remain available.
`

type Options struct {
	exactDestination                                     bool
	fileProgress                                         *fileProgress
	deferHistory                                         bool
	Host, Path, To, Session, Theme                       string
	Last, Yes, Overwrite, DryRun, NoHistory, Track, JSON bool
	SkipReview                                           bool
	Sort                                                 SortOrder
	Progress                                             *batchProgress
	SSH                                                  []string
	Pos                                                  []string
}

func parseOptions(args []string) (Options, error) {
	o := Options{To: "."}
	sortField, sortDirection := "", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		take := func() (string, error) {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s requires a value", a)
			}
			return args[i], nil
		}
		switch a {
		case "--host", "--path", "--to", "--session", "--theme":
			v, e := take()
			if e != nil {
				return o, e
			}
			switch a {
			case "--theme":
				o.Theme, e = themeName(v)
				if e != nil {
					return o, e
				}
			case "--host":
				o.Host = v
			case "--path":
				o.Path = v
			case "--to":
				o.To = v
			case "--session":
				o.Session = v
			}
		case "--sort", "--order":
			v, e := take()
			if e != nil {
				return o, e
			}
			if a == "--sort" {
				sortField = v
			} else {
				sortDirection = v
			}
		case "--last":
			o.Last = true
		case "--yes", "-y":
			o.Yes = true
		case "--overwrite":
			o.Overwrite = true
		case "--dry-run":
			o.DryRun = true
		case "--no-history":
			o.NoHistory = true
		case "--track":
			o.Track = true
		case "--json":
			o.JSON = true
		case "-p", "-i", "-J", "-F", "-l":
			v, e := take()
			if e != nil {
				return o, e
			}
			if a == "-i" || a == "-F" {
				v = homeExpand(v)
			}
			if strings.ContainsAny(v, "\r\n\x00") {
				return o, errors.New("invalid SSH option")
			}
			o.SSH = append(o.SSH, a, v)
		case "--":
			o.Pos = append(o.Pos, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(a, "-") {
				return o, fmt.Errorf("unknown option %s (see hop --help)", a)
			}
			o.Pos = append(o.Pos, a)
		}
	}
	var sortErr error
	o.Sort, sortErr = parseSort(sortField, sortDirection)
	if sortErr != nil {
		return o, sortErr
	}
	if o.Host != "" && o.Last {
		return o, errors.New("choose --host or --last")
	}
	if o.Session != "" && (o.Host != "" || o.Last) {
		return o, errors.New("--session already selects a machine")
	}
	return o, nil
}
func selectHost(o Options, d Discovery, s State) (Host, *Session, error) {
	if o.Session != "" {
		sessions := append([]Session{}, s.Sessions...)
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].Updated > sessions[j].Updated })
		for _, session := range sessions {
			if (o.Session == "latest" || o.Session == session.ID) && sessionAlive(session) {
				if session.CWD == "" {
					return Host{}, nil, errors.New("session has not reported a working directory yet")
				}
				return session.Host, &session, nil
			}
		}
		return Host{}, nil, errors.New("no matching active tracked session; start hop ssh --track first")
	}
	if o.Host != "" {
		target, opts, ok := normalizeTarget(o.Host)
		if !ok {
			return Host{}, nil, errors.New("invalid SSH destination")
		}
		for _, h := range d.Hosts {
			if h.Target == target && len(opts) == 0 {
				return h, nil, nil
			}
		}
		return Host{Target: target, Options: opts, Source: "explicit destination"}, nil, nil
	}
	if len(d.Hosts) == 0 && o.Last {
		return Host{}, nil, errors.New("no recent machines; start hop to enter an SSH destination")
	}
	if o.Last {
		return d.Hosts[0], nil, nil
	}
	resolveHostDetails(d.Hosts, o.SSH)
	items := []PickItem{}
	for _, h := range d.Hosts {
		items = append(items, PickItem{Label: h.Target, Detail: hostDetail(h), Value: h.Key()})
	}
	r, e := (Picker{HostInput: true, Title: "Choose a machine", Caption: "MACHINES  Recent SSH connections", Badge: "SSH", Items: items, Status: fmt.Sprintf("%d machines", len(items))}).Run()
	if e != nil {
		return Host{}, nil, e
	}
	if r.Action == "literal" {
		target, opts, _ := normalizeTarget(r.Item.Value)
		return Host{Target: target, Options: opts}, nil, nil
	}
	for _, h := range d.Hosts {
		if h.Key() == r.Item.Value {
			return h, nil, nil
		}
	}
	return Host{}, nil, errors.New("machine not found")
}
func withOverrides(h Host, overrides []string) Host {
	if len(overrides) == 0 {
		return h
	}
	replaced := map[string]bool{}
	for i := 0; i+1 < len(overrides); i += 2 {
		replaced[overrides[i]] = true
	}
	remaining := []string{}
	for i := 0; i < len(h.Options); i++ {
		key := h.Options[i]
		if connectionFlags[key] || key == "-o" {
			if i+1 < len(h.Options) {
				if !replaced[key] {
					remaining = append(remaining, key, h.Options[i+1])
				}
				i++
			}
		} else {
			remaining = append(remaining, key)
		}
	}
	h.Options = append(append([]string{}, overrides...), remaining...)
	return h
}
func run(args []string) (runErr error) {
	if len(args) == 0 {
		args = []string{"browse"}
	}
	if args[0] == "--help" || args[0] == "help" || contains(args, "--help") {
		fmt.Print(help)
		return nil
	}
	if args[0] == "--version" {
		fmt.Println("hop", version)
		return nil
	}
	if !contains([]string{"browse", "send", "get", "ssh", "hosts", "doctor", "shell-init", "record", "demo", "config"}, args[0]) {
		args = append([]string{"browse"}, args...)
	}
	command := args[0]
	if command == "config" {
		return configure(args[1:])
	}
	if command == "shell-init" {
		if len(args) != 2 || !contains([]string{"zsh", "bash"}, args[1]) {
			return errors.New("usage: hop shell-init zsh|bash")
		}
		fmt.Print("ssh() { command hop record -- \"$@\" >/dev/null 2>&1; command ssh \"$@\"; }\n")
		return nil
	}
	if command == "record" {
		words := args[1:]
		if len(words) > 0 && words[0] == "--" {
			words = words[1:]
		}
		h, _, ok := parseConnection(append([]string{"ssh"}, words...))
		if !ok {
			return nil
		}
		if e := initDataDir(); e != nil {
			return e
		}
		return rememberHost(h)
	}
	o, e := parseOptions(args[1:])
	if e != nil {
		return e
	}
	if e = loadTheme(o.Theme); e != nil {
		return e
	}
	if command == "doctor" && len(args) == 2 && args[1] == "mouse" {
		return diagnoseMouse()
	}
	if command == "demo" {
		return demoManager(o.Sort)
	}
	if command == "browse" {
		return runManager(o)
	}
	if !contains([]string{"send", "get", "ssh", "hosts", "doctor"}, command) {
		return fmt.Errorf("unknown command %q; see hop --help", command)
	}
	if e = initDataDir(); e != nil {
		return e
	}
	s, e := readState()
	if e != nil {
		return e
	}
	d := discover(s)
	if command == "doctor" {
		for _, name := range []string{"/usr/bin/ssh", "/bin/stty"} {
			_, e := os.Stat(name)
			fmt.Printf("%s  %v\n", name, e == nil)
		}
		colorMode := terminalColorMode()
		if !colorEnabled() {
			colorMode = "disabled (NO_COLOR or TERM=dumb)"
		}
		fmt.Printf("Theme colors: %s\n", colorMode)
		fmt.Printf("Theme: %s\n", themeLabel())
		if strings.Contains(strings.ToLower(os.Getenv("TERM_PROGRAM")), "warp") {
			fmt.Println("Warp mouse: enable Mouse Reporting and Scroll Reporting in Settings > Features > Terminal")
		}
		fmt.Printf("State: %s\nMachines discovered: %d\nInteractive terminal: %v\n", dataDir, len(d.Hosts), ttyAvailable())
		return nil
	}
	if command == "hosts" {
		if o.JSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(d.Hosts)
		}
		resolveHostDetails(d.Hosts, o.SSH)
		for i, h := range d.Hosts {
			fmt.Printf("%2d  %-30s  %s\n", i+1, safeText(h.Target), safeText(hostDetail(h)))
		}
		if len(d.Hosts) == 0 {
			fmt.Println("No SSH destinations found.")
		}
		return nil
	}
	if command == "send" || command == "get" {
		screen := beginFullscreen()
		defer func() {
			screen.Close()
			if screen != nil && runErr == nil {
				if o.DryRun {
					fmt.Println("Dry run complete. No files copied.")
				} else {
					fmt.Println("✓ Copy complete.")
				}
			}
		}()
	}
	local := ""
	var sourceFiles []FileItem
	if command == "send" {
		if o.To != "." {
			return errors.New("use --path for a remote upload destination; --to is for local downloads")
		}
		sourceFiles, e = localSources(o.Pos, o.Sort)
		if e != nil {
			return e
		}
		if len(sourceFiles) == 0 {
			return errCancelled
		}
		local = sourceFiles[0].Path
	} else {
		if len(o.Pos) > 1 {
			return errors.New("expected at most one machine name")
		}
		if len(o.Pos) == 1 {
			if o.Host != "" || o.Last || o.Session != "" {
				return errors.New("machine specified more than once")
			}
			o.Host = o.Pos[0]
		}
	}
	h, session, e := selectHost(o, d, s)
	if e != nil {
		return e
	}
	h = withOverrides(h, o.SSH)
	if o.Session != "" && len(o.SSH) > 0 {
		return errors.New("connection overrides cannot be combined with --session")
	}
	if session != nil {
		s.Sessions = []Session{*session}
	}
	if command == "ssh" {
		if o.DryRun || o.Overwrite || o.Yes || o.Path != "" || o.To != "." {
			return errors.New("copy options are not supported by hop ssh")
		}
		return runSSH(h, o.Track)
	}
	if o.Track {
		return errors.New("--track is only for hop ssh")
	}
	connectCtx, stopConnect := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopConnect()
	connecting := startActivity("Connecting to "+safeText(h.Label()), 0, false)
	sftp, e := connectSFTPContext(connectCtx, h)
	connecting.Stop()
	if connectCtx.Err() != nil {
		return errCancelled
	}
	if e != nil {
		return e
	}
	defer sftp.Close()
	if e = rememberHost(h); e != nil {
		return e
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigs:
			sftp.Abort()
		case <-done:
		}
	}()
	remote := o.Path
	if remote == "" && session != nil && command == "send" {
		remote = session.CWD
	}
	if command == "send" {
		if remote == "" {
			result, err := browseRemote(sftp, h, s, d, local, true, o.NoHistory, sourceFiles, o.DryRun, o.Sort)
			if err != nil {
				return err
			}
			remote = result.Directory
		}
		if len(sourceFiles) == 1 && !sourceFiles[0].Dir && o.Path != "" {
			if rsyncSingleCandidate(o, sourceFiles[0].Size) {
				if dest, ok := rsyncUploadDirectory(sftp, sourceFiles[0].Path, remote); ok {
					return copyBatch(sftp, h, sourceFiles, dest, false, o)
				}
			}
			return sendFile(sftp, h, sourceFiles[0].Path, remote, o)
		}
		return copyBatch(sftp, h, sourceFiles, remote, false, o)
	}
	if remote != "" {
		home, err := sftp.Realpath(".")
		if err != nil {
			return err
		}
		remote = expandRemote(remote, home)
		a, err := sftp.Stat(remote, false)
		if err != nil {
			return err
		}
		if a.Dir() {
			return copyBatch(sftp, h, []FileItem{{Name: path.Base(remote), Path: remote, Dir: true}}, o.To, true, o)
		}
		if rsyncSingleCandidate(o, a.Size) {
			if dest, ok := rsyncDownloadDirectory(remote, o.To); ok {
				return copyBatch(sftp, h, []FileItem{{Name: path.Base(remote), Path: remote, Regular: true}}, dest, true, o)
			}
		}
		return getFile(sftp, h, remote, o.To, o)
	}
	result, err := browseRemote(sftp, h, s, d, "", false, o.NoHistory, nil, o.DryRun, o.Sort)
	if err != nil {
		return err
	}
	return copyBatch(sftp, h, result.Files, o.To, true, o)
}
func demo(sorting SortOrder) error {
	root := "/home/demo/releases"
	now := time.Now().Unix()
	loader := func(p string) ([]FileItem, error) {
		if p == root+"/private" {
			return nil, &StatusError{Code: 3, Message: "Permission denied"}
		}
		items := []FileItem{}
		if p == root {
			items = []FileItem{
				{Name: "exports", Path: p + "/exports", Dir: true, Modified: now},
				{Name: "scripts", Path: p + "/scripts", Dir: true, Modified: now - 240},
				{Name: "build.zip", Path: p + "/build.zip", Regular: true, Size: 8600000, Modified: now - 360},
				{Name: "config.yaml", Path: p + "/config.yaml", Regular: true, Size: 2150, Modified: now - 480},
				{Name: "report.csv", Path: p + "/report.csv", Regular: true, Size: 780288, Modified: now - 720},
				{Name: "notes.md", Path: p + "/notes.md", Regular: true, Size: 1229, Modified: now - 86400},
			}
		} else {
			items = []FileItem{{Name: "report.csv", Path: p + "/report.csv", Regular: true, Size: 42000, Modified: now}}
		}
		return sortedListing(p, items), nil
	}
	result, e := (Browser{Sort: sorting, Title: "Pick files · DEMO", Host: "server (demo)", Current: root, Home: root, Load: loader, Recent: []FileItem{{Name: root, Path: root, Dir: true}}}).Run()
	if e != nil {
		return e
	}
	fmt.Printf("Demo: %d files selected. No connection or transfer made.\n", len(result.Files))
	return nil
}
func main() {
	if e := run(os.Args[1:]); e != nil {
		if errors.Is(e, errCancelled) {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			os.Exit(130)
		}
		var ex *exec.ExitError
		if errors.As(e, &ex) {
			fmt.Fprintln(os.Stderr, "hop:", e)
			os.Exit(max(1, ex.ExitCode()))
		}
		fmt.Fprintln(os.Stderr, "hop:", e)
		os.Exit(1)
	}
}
