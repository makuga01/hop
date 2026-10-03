# Using Hop

[Back to the README](../README.md)

## Connect

```sh
hop                                       # Choose a server
hop user@hostname                         # Connect directly
hop my-server                             # Use an SSH alias
hop --last                                # Use the most recent server
hop my-server --to ~/Projects --path /srv   # Set both starting folders
hop hosts                                 # List known servers
hop demo                                  # Browse fictional files offline
```

Hop uses OpenSSH and SFTP. The server needs an SSH server with SFTP enabled.
SSH config, keys, agent authentication, and jump hosts work as they do with `ssh`.
The machine picker shows the resolved login user, hostname, and any port other than 22.
You can filter by alias or destination, or type a new SSH destination.

The terminal must be at least 64 columns by 18 rows; 100 columns gives filenames more room.
The installer downloads the latest release for your platform, verifies SHA-256,
and installs the binary to `~/.local/bin/hop`. It doesn't use sudo, edit shell
profiles, or change PATH. It replaces an existing regular `hop` file only after
the download and binary checks succeed; symlinks and directories are left alone.
Temporary download files are removed when it exits.

If `hop` isn't found, launch `~/.local/bin/hop`. You can add `~/.local/bin` to PATH
yourself if you want; the installer won't do it for you.

To choose a different installation directory:

```sh
curl -fsSL https://raw.githubusercontent.com/makuga01/hop/main/scripts/install.sh | HOP_INSTALL_DIR=/your/bin sh
```

The installer requires a public GitHub release and common system tools: curl,
tar, and either `sha256sum` or `shasum`. It doesn't require Go or Python.

## Keyboard reference

These shortcuts apply when a text field isn't active.

| Key | Action |
| --- | --- |
| Tab | Switch panels |
| ↑ / ↓ or `k` / `j` | Move the cursor |
| Enter, →, or `l` | Open a folder; Enter on a file toggles its selection |
| Backspace, ←, `H`, or `u` | Go to the parent folder |
| Home or `gg` | First item |
| End or `G` | Last item |
| Space | Select or deselect an item without moving the cursor |
| Esc | Clear the active panel's selections |
| `c`, `y`, Ctrl-S, or Ctrl-T | Copy to the other panel |
| `m` | Move to the other panel |
| `D` or `dd` | Delete |
| `n` or Ctrl-N | Create a folder |
| `P` or Ctrl-L | Enter a path |
| `b` or Ctrl-B | Switch servers |
| `r` or Ctrl-R | Recent paths |
| `R` or Ctrl-E | Refresh both panels |
| `s` or Ctrl-O | Change sort order |
| `.` or Ctrl-H | Toggle hidden files |
| `/` | Filter filenames |
| Ctrl-U | Clear the filter |
| `o` | Settings and connection options |
| `h` or `?` | Scrollable shortcut reference |
| `q` | Quit |
| Ctrl-C | Cancel the active operation, or quit when idle |

Type `dd` or `gg` within one second. In a text field or confirmation prompt,
Esc closes that first. With no selections, Esc clears the filter or status message.
Press Enter or Esc to leave a filter field; the filter stays until cleared.

Each panel keeps its own cursor, sorting, selections, and current folder.
Selections survive folder changes. The selection box lists exactly what will be copied.

## Mouse

- Click the inactive panel to focus it. Click an item in the active panel to move the cursor.
- Double-click a folder to open it; right-click an item to toggle selection.
- Click a column heading to sort; click again to reverse it.
- The wheel moves the cursor in the active panel. Drag the scrollbar to scroll.

## Copy, move, and delete

The active panel is the source; the other panel's current folder is the destination.
With nothing selected, the operation uses the item under the cursor.

Copies include nested folders, hidden files, and empty folders. Source symlinks are
followed; their targets are copied under the link names. Broken links, loops, and
special files stop the preflight check. Existing destination symlinks and mismatched
file types aren't overwritten.

For faster download scans, Hop can use an existing Python 3 over noninteractive
SSH to list the tree and check read access on the server. Only compressed metadata
comes back; no helper is installed and no file contents are read during the scan.
If the helper is unavailable or its output cannot be validated, Hop uses SFTP.
Destination checks and transfer-time validation still apply.

When filenames conflict, choose **Replace** for that transfer, **Options** to change
the session's overwrite setting, or **Cancel**. Folder contents merge.
Copies of at least 256 MiB or 200 items ask for confirmation before starting.

Confirmation dialogs support arrow keys and Tab to select a button. The selected
button is highlighted and marked with `> … <`. Enter activates that button;
Esc cancels. Clicking a button activates that exact action.

For new downloads and confirmed small-file replacements (up to 1 MiB), Hop can
stream a batch over one SSH connection using an existing Python 3, when at least
32 files qualify. Each file carries a
SHA-256 checksum. New or changed files are synced to temporary local files and
committed atomically. Existing destinations require explicit replacement
authorization; identical file contents are compared and left
in place without rewriting. Existing small-file SHA-256 hashes are sent first,
so identical contents are not downloaded again. The stream uses gzip in both
directions, including the request list. New large files use bounded buffers and
temporary files;
existing large files keep the delta/SFTP path. Repeated remote file identities
can reuse an already verified local copy, with another checksum check and a
separate destination file. The progress panel shows whether Stream or SFTP is
active. If the helper is unavailable before streaming begins, Hop falls back to
SFTP. An interrupted stream stops the batch; files already committed remain, as with ordinary copies.

A move copies and verifies the whole selection before removing the sources. Failed
copies leave the sources in place. Changes to the source after scanning stop removal.

Deletion is permanent and includes hidden files inside selected folders. Deleting a
symlink removes the link, not its target. If deletion is interrupted, some items may
already be gone. Moves and deletes always require confirmation.

```sh
hop my-server --dry-run     # Preview operations without changing files
hop my-server --overwrite   # Allow replacing destination files
hop my-server --yes         # Skip large-copy confirmation (not move/delete confirmation)
```

SFTP transfers overlap requests and copy multiple files concurrently. Eligible
download batches use a separate compressed SSH stream. Uploads, existing large
files, and servers without the helper use SFTP, with native delta reuse where
available. No remote software installation is required.
If a connection or transfer fails, the panels stay open with Retry and Options available.

## Faster repeat transfers

When replacing files of at least 1 MiB, Hop can reuse matching blocks and send
only the changed ranges. It verifies the assembled file with SHA-256 before
replacing the destination. No rsync or remote installation is needed.

The fast path needs noninteractive SSH with a POSIX shell and either Python 3
or `dd` plus `sha256sum`, `shasum`, or OpenSSL. Upload reuse also needs the
server's `copy-data` SFTP extension. Missing capabilities fall back to ordinary
pipelined SFTP, including on SFTP-only servers. Fresh files use SFTP directly.

Block signatures start at 64 KiB. When Python is available, a rolling checksum
search can reuse data shifted by insertions or deletions; SHA-256 confirms every
match. The shell-only helper uses fixed blocks. Files with less than half their
bytes reusable use a full transfer. Fresh copies and complete replacements do
not gain from delta reuse.

Set `HOP_TRANSFER_BACKEND=sftp` to force full SFTP transfers. The older rsync
experiment remains available only with `HOP_TRANSFER_BACKEND=rsync`; Hop does
not look for or run rsync by default.

## Settings, history, and themes

Press `o` to change overwrite behavior, copy confirmations, preview mode, remote history,
theme, or sorting. You can also edit the SSH destination, port, identity file, jump host,
and config file, then reconnect.

The server list reads local SSH config, shell history, and previous connections.
Recent paths also use transfer history and remote shell history.
`--no-history` disables reading remote shell history; it does not disable local discovery.

| Platform | Config and history location |
| --- | --- |
| macOS | `~/Library/Application Support/Hop/` |
| Linux | `$XDG_CONFIG_HOME/Hop/`, or `~/.config/Hop/` if unset |

`HOP_HOME` overrides the directory. Settings are in `config.json`; history is in `state.json`.

```sh
hop config theme dark      # Save the default dark theme
hop --theme light          # Choose a theme for this session
hop my-server --sort date
hop my-server --sort name --order desc
```

Themes: `dark` (default), `light`, `auto`, `black` (pure black), `lagoon`, `cobalt`,
and `afterhours`. Dark and Light use neutral surfaces with a muted blue selection.
Options changes the theme for this session; `hop config theme NAME` saves it.

Auto first uses the background advertised in `COLORFGBG`, then the macOS system
appearance or GNOME's explicit light/dark preference. When no preference is
available, it uses Dark. Custom terminal palettes can differ from the system:
use `--theme light` or `--theme dark` to override the guess. Existing saved themes
keep their settings. Detection runs at startup and does not change terminal settings.
Hop uses true color when available, with a 256-color fallback for Apple Terminal.
Set `HOP_COLOR_MODE` to `truecolor` or `256` to override detection. `NO_COLOR` disables colors.

### Upgrading from Hops

If Hop's settings files don't exist, it reads the former Hops settings.
`HOPS_HOME` and `HOPS_COLOR_MODE` still work; the `HOP_*` values take precedence.
Keep a separate `HOP_HOME` if you also use the older single-panel `hop-classic`.

### Mouse and scrolling in Warp

In Warp, enable **Settings → Features → Terminal → Enable Mouse Reporting**, then
**Scroll Reporting**. Without those settings, Warp keeps the events instead of
sending them to Hop. Holding Shift also gives the mouse to Warp for text selection.
See [Warp's full-screen app settings](https://docs.warp.dev/terminal/more-features/full-screen-apps).
Hop supports SGR mouse events and legacy mouse packets, and disables mouse reporting
when leaving the interface. `hop doctor` includes this reminder when run in Warp.
If both settings are on and input still fails, run `hop doctor mouse`, click and
scroll in the test area, then press Q. It reports event counts and protocol names
without recording typed text, file paths, or connecting to a server.

## Development

Application code and tests live in `cmd/hop/`; terminal test fixtures are in
`cmd/hop/testdata/`. Build and recording scripts live in `scripts/`.

```sh
make build   # Build ./hop
make check   # Race tests, vet, and formatting checks
make dist    # Build archives for macOS/Linux on AMD64/ARM64
```

Tests need Python 3 and a local OpenSSH SFTP server. On Debian/Ubuntu, install
`python3` and `openssh-sftp-server`. Tests use local processes and pseudo-terminals.
See [the release guide](releasing.md) for publishing and [the media guide](media/README.md)
for recreating the demo recordings.
