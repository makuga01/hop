package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type managerAction struct {
	host  Host
	kind  string
	side  int
	files []FileItem
}
type dualManager struct {
	settings                       bool
	details                        string
	detailOffset                   int
	settingsCursor, settingsOffset int
	settingsEdit                   bool
	settingsText, settingsError    string
	connection                     [5]string
	clickPath                      string
	clickSide                      int
	clickAt                        time.Time
	filtering                      [2]bool
	help                           bool
	helpOffset                     int
	prefix                         string
	prefixAt                       time.Time
	sftp                           *SFTP
	options                        Options
	transfer                       *panelTransfer
	panes                          [2]*browserModel
	homes                          [2]string
	loaders                        [2]FileLoader
	mkdir                          [2]func(string) error
	host                           Host
	active                         int
	notice                         string
	demo                           bool
	readonly                       bool
	next                           [2]string
}

func newManager(local, remote string, h Host, s *SFTP, order SortOrder) *dualManager {
	home, _ := os.UserHomeDir()
	d := &dualManager{sftp: s, host: h, homes: [2]string{home, remote}}
	d.panes[0] = newBrowserModel(Browser{Current: local, Sort: order})
	d.panes[1] = newBrowserModel(Browser{Current: remote, Sort: order})
	for _, pane := range d.panes {
		pane.hideDotfiles = true
	}
	d.loaders = [2]FileLoader{localFileLoader, remoteFileLoader(s)}
	if s == nil {
		d.loaders[1] = func(string) ([]FileItem, error) {
			return nil, errors.New("not connected; press o for Options or b to choose a machine")
		}
	}
	d.mkdir = [2]func(string) error{func(p string) error { return os.Mkdir(p, 0755) }, func(p string) error {
		if s == nil {
			return errors.New("not connected; press o for Options")
		}
		return s.Mkdir(p, 0755)
	}}
	return d
}

func managerHostOptions(o Options) (Options, error) {
	if len(o.Pos) > 1 {
		return o, errors.New("expected at most one SSH machine")
	}
	if len(o.Pos) == 1 {
		if o.Host != "" || o.Last || o.Session != "" {
			return o, errors.New("machine specified more than once")
		}
		o.Host = o.Pos[0]
	}
	return o, nil
}
func managerConnect(h Host) (*SFTP, string, context.CancelFunc, error) {
	return managerConnectUsing(h, connectSFTPContext)
}
func managerConnectUsing(h Host, connect func(context.Context, Host) (*SFTP, error)) (*SFTP, string, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-signals:
			cancel()
		case <-stop:
		}
	}()
	defer func() { signal.Stop(signals); close(stop); <-stopped }()
	busy := startActivity("Connecting to "+h.Label(), 0, false)
	s, err := connect(ctx, h)
	busy.Stop()
	if err != nil {
		cancel()
		return nil, "", nil, err
	}
	busy = startActivity("Opening remote home", 0, false)
	home, err := s.Realpath(".")
	busy.Stop()
	if err != nil {
		s.Close()
		cancel()
		return nil, "", nil, err
	}
	if err = rememberHost(h); err != nil {
		s.Close()
		cancel()
		return nil, "", nil, err
	}
	return s, home, cancel, nil
}
func runManager(o Options) error {
	var err error
	o, err = managerHostOptions(o)
	if err != nil {
		return err
	}
	if o.Track {
		return errors.New("--track is only supported by hop ssh")
	}
	local, err := filepath.Abs(homeExpand(o.To))
	if err != nil {
		return err
	}
	info, err := os.Stat(local)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("--to must be a local directory")
	}
	if err = initDataDir(); err != nil {
		return err
	}
	state, err := readState()
	if err != nil {
		return err
	}
	discovery := discover(state)
	screen := beginFullscreen()
	if screen == nil {
		return errors.New("hop requires an interactive terminal")
	}
	defer screen.Close()
	h, session, err := selectHost(o, discovery, state)
	if err != nil {
		return err
	}
	h = withOverrides(h, o.SSH)
	s, home, closeConnection, err := managerConnect(h)
	connectionNotice := ""
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, errCancelled) {
			return err
		}
		connectionNotice = "Connection failed: " + err.Error() + " · o Options to reconnect"
		home = "/"
		closeConnection = func() {}
	}
	defer func() {
		if s != nil {
			s.Close()
		}
		closeConnection()
	}()
	remote := home
	if session != nil && session.CWD != "" {
		remote = session.CWD
	}
	if o.Path != "" {
		remote = expandRemote(o.Path, home)
		if !path.IsAbs(remote) {
			remote = path.Join(home, remote)
		}
	}
	d := newManager(local, remote, h, s, o.Sort)
	d.homes[1] = home
	d.readonly = o.DryRun
	d.options = o
	d.notice = connectionNotice
	for {
		action, err := d.Run()
		if err != nil {
			return err
		}
		o = d.options
		switch action.kind {
		case "quit":
			return nil
		case "recent":
			target, e := managerRecent(d, action.side, s, o)
			if e == nil && target != "" {
				d.next[action.side] = target
			}
			if e != nil && !errors.Is(e, errCancelled) {
				d.notice = e.Error()
			}
		case "machine", "connect", "reconnect":
			next := action.host
			var e error
			if action.kind == "machine" {
				state, e := readState()
				if e != nil {
					d.notice = e.Error()
					continue
				}
				next, _, e = selectHost(Options{}, discover(state), state)
				if e != nil {
					if !errors.Is(e, errCancelled) {
						d.notice = e.Error()
					}
					continue
				}
				next = withOverrides(next, o.SSH)
			}
			if action.kind == "reconnect" {
				if s != nil {
					s.Close()
				}
				closeConnection()
				s, d.sftp = nil, nil
				d.loaders[1] = func(string) ([]FileItem, error) { return nil, errors.New("not connected; press o to reconnect") }
				d.mkdir[1] = func(string) error { return errors.New("not connected; press o to reconnect") }
			}
			connection, newHome, newCancel, e := managerConnect(next)
			if e != nil {
				d.notice = "Connection failed: " + e.Error()
				continue
			}
			if s != nil {
				s.Close()
			}
			closeConnection()
			closeConnection = newCancel
			s = connection
			d.host = next
			d.connection = [5]string{}
			if action.kind != "reconnect" {
				d.transfer = nil
			}
			d.sftp = s
			d.homes[1] = newHome
			if action.kind != "reconnect" {
				d.panes[1] = newBrowserModel(Browser{Current: newHome, Sort: o.Sort})
				d.panes[1].hideDotfiles = true
			}
			d.loaders[1] = remoteFileLoader(s)
			current := s
			d.mkdir[1] = func(p string) error { return current.Mkdir(p, 0755) }
			if action.kind != "reconnect" {
				d.notice = "Connected to " + next.Label()
			}
		}
	}
}

func managerRecent(d *dualManager, side int, s *SFTP, o Options) (string, error) {
	m := d.panes[side]
	candidates := append([]FileItem{}, m.recent...)
	candidates = append(candidates, FileItem{Path: d.homes[side], Name: d.homes[side], Dir: true})
	toPicks := func(files []FileItem) []PickItem {
		seen := map[string]bool{}
		var out []PickItem
		for _, f := range files {
			if f.Dir && !seen[f.Path] {
				seen[f.Path] = true
				out = append(out, PickItem{Label: f.Path, Value: f.Path})
			}
		}
		return out
	}
	if side == 0 {
		r, e := (Picker{Title: "Recent local folders", Caption: "LOCAL FOLDERS  Recent locations", Badge: "LOCAL", Items: toPicks(candidates)}).Run()
		return r.Item.Value, e
	}
	state, e := readState()
	if e != nil {
		return "", e
	}
	discovery := discover(state)
	base := initialCandidates(d.host, state, discovery, d.panes[0].current, true)
	base = append(base, Candidate{Path: d.homes[1], Dir: true, Score: 15})
	candidates = append(candidates, recentFiles(base)...)
	updates := make(chan PickUpdate, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		publish := func(cs []Candidate, _ string) {
			update := PickUpdate{Items: toPicks(append(candidates, recentFiles(cs)...))}
			select {
			case updates <- update:
			default:
				select {
				case <-updates:
				default:
				}
				select {
				case updates <- update:
				default:
				}
			}
		}
		fresh := refreshContext(ctx, s, d.homes[1], base, true, o.NoHistory, publish)
		if ctx.Err() == nil {
			_ = updateState(func(st *State) {
				st.Caches[d.host.Key()] = Cache{Candidates: fresh, Home: d.homes[1], At: time.Now().Unix()}
			})
		}
	}()
	r, e := (Picker{Title: "Recent remote folders", Caption: "REMOTE FOLDERS  Recent locations", Badge: "REMOTE", Items: toPicks(candidates), Updates: updates}).Run()
	cancel()
	select {
	case <-done:
	default:
		busy := startActivity("Finishing lookup", 0, false)
		<-done
		busy.Stop()
	}
	return r.Item.Value, e
}

func demoManager(order SortOrder) error {
	if !ttyAvailable() {
		return errors.New("hop demo requires a terminal")
	}
	screen := beginFullscreen()
	defer screen.Close()
	d := newManager("/Users/demo/Projects", "/home/demo/releases", Host{Target: "server (demo)"}, nil, order)
	d.demo = true
	loader := func(root string) FileLoader {
		return func(p string) ([]FileItem, error) {
			var items []FileItem
			if p == root {
				items = []FileItem{{Name: "exports", Path: path.Join(p, "exports"), Dir: true}, {Name: "scripts", Path: path.Join(p, "scripts"), Dir: true}, {Name: "build.zip", Path: path.Join(p, "build.zip"), Regular: true, Size: 8600000}, {Name: "config.yaml", Path: path.Join(p, "config.yaml"), Regular: true, Size: 2150}, {Name: "report.csv", Path: path.Join(p, "report.csv"), Regular: true, Size: 780288}}
			} else {
				items = []FileItem{{Name: "summary.csv", Path: path.Join(p, "summary.csv"), Regular: true, Size: 2400}}
			}
			return sortedListing(p, items), nil
		}
	}
	for i := 0; i < 2; i++ {
		d.loaders[i] = loader(d.panes[i].current)
	}
	for {
		a, e := d.Run()
		if e != nil {
			return e
		}
		if a.kind == "quit" {
			return nil
		}
		d.notice = "Offline demo: connections, copies, and folder creation are disabled"
	}
}

func managerKey(data []byte) (string, int) {
	if len(data) > 0 && len(data) < len(pasteStart) && strings.HasPrefix(pasteStart, string(data)) {
		return "", 0
	}
	if len(data) > 0 {
		switch data[0] {
		case 9:
			return "panel", 1
		case 19, 20:
			return "copy", 1
		case 8:
			return "hidden", 1
		case 2:
			return "machine", 1
		case 5:
			return "refresh", 1
		}
	}
	return browserKey(data)
}
func (d *dualManager) selectedAction() managerAction {
	m := d.panes[d.active]
	files := m.selected()
	if len(files) == 0 {
		v := m.visible()
		if m.cursor >= 0 && m.cursor < len(v) && markable(v[m.cursor]) {
			files = []FileItem{v[m.cursor]}
		}
	}
	return managerAction{kind: "copy", side: d.active, files: files}
}
func (d *dualManager) toggleFocused() {
	m := d.panes[d.active]
	v := m.visible()
	if m.cursor >= 0 && m.cursor < len(v) && markable(v[m.cursor]) {
		m.toggle(v[m.cursor])
		m.notice = ""
	}
}
func (d *dualManager) paneLabel(side int) string {
	label := "LOCAL"
	if side == 1 {
		label = "REMOTE · " + d.host.Label()
	}
	if side == d.active {
		return "● " + label
	}
	return "  " + label
}
func managerPath(current, home, value string) string {
	value = expandRemote(value, home)
	if !strings.HasPrefix(value, "/") {
		value = path.Join(current, value)
	}
	return path.Clean(value)
}

func (d *dualManager) toggleHidden() {
	m := d.panes[d.active]
	v := m.visible()
	focused := ""
	if m.cursor >= 0 && m.cursor < len(v) {
		focused = v[m.cursor].Path
	}
	m.hideDotfiles = !m.hideDotfiles
	v = m.visible()
	m.cursor = min(m.cursor, max(0, len(v)-1))
	for i, f := range v {
		if f.Path == focused {
			m.cursor = i
			break
		}
	}
	d.notice = "Hidden files shown"
	if m.hideDotfiles {
		d.notice = "Hidden files hidden"
	}
}
