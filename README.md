# Hop

An SSH-only file manager for macOS and Linux with a persistent two-panel terminal workspace, inspired by [termscp](https://github.com/veeso/termscp). Local files stay on the left; your selected SSH machine stays on the right. SFTP runs through your system's OpenSSH. Nothing is installed on the remote machine.

## Install

Download the archive matching your machine from this repository's GitHub Releases:

| Platform | Archive suffix |
| --- | --- |
| macOS, Apple Silicon | `darwin_arm64.tar.gz` |
| macOS, Intel | `darwin_amd64.tar.gz` |
| Linux, Intel/AMD | `linux_amd64.tar.gz` |
| Linux, ARM64 | `linux_arm64.tar.gz` |

Extract the archive, then install the executable on your PATH:

```sh
tar -xzf hop_0.1.0_darwin_arm64.tar.gz  # choose your platform's archive
mkdir -p ~/.local/bin
install -m 755 hop ~/.local/bin/hop
hop --version
```

Add `export PATH="$HOME/.local/bin:$PATH"` to your shell startup file if needed.
Download `SHA256SUMS` alongside the archive and compare its entry using
`shasum -a 256 <archive>` on macOS or `sha256sum <archive>` on Linux.
Requires `ssh`, `stty`, and an interactive terminal; the remote SSH server must support
SFTP. No Go or Python installation is needed to run Hop.

## Start

```sh
hop                         # Pick a machine from recent SSHs and SSH config
hop my-server                    # Use an SSH alias or user@address
hop --last                  # Most recent discovered machine
hop my-server --to ~/Projects --path /srv/app
hop demo                    # Offline two-panel demo; no network or file writes
```

The initial connection uses your SSH config, agent, keys, jump hosts, and native authentication prompts. Ctrl-B or the Machine button selects another machine. A failed new connection keeps the previous session; a successful change clears the old remote selection and opens the new machine's home directory. Local selections stay available.

## Controls

| Key | Action |
| --- | --- |
| o / Options button | Change session options and SSH connection settings |
| `/` / click Filter | Focus the highlighted filter bar |
| Esc / Enter in filter | Leave filter mode, keeping the query |
| Esc in normal mode | Deselect all items in the active panel; then clear filter/status |
| Ctrl-U | Clear the filter |
| j / k / arrows / wheel | Move the active panel cursor, regardless of mouse position |
| H / u / Left / Backspace | Parent folder |
| l / Right / Enter | Open folder; Enter marks a file |
| gg / G / Home / End | First / last entry |
| Tab / click inactive panel | Switch focus only; keep cursor and marks |
| Space / right click | Mark/unmark in place |
| Single / double-click | Move cursor / open folder in the active panel |
| c / y / Ctrl-S / Ctrl-T | Copy to the opposite panel |
| m | Move to the opposite panel |
| dd / D | Delete marked items, or the focused item |
| n / Ctrl-N | Create and open a folder |
| P / Ctrl-L | Enter a path |
| b / Ctrl-B | Pick another SSH machine |
| r / Ctrl-R | Recent directories for the active panel |
| R / Ctrl-E | Refresh both panels |
| s / Ctrl-O | Cycle name, date, size, and type sorting |
| Click column | Sort that column; click again to reverse |
| . / Ctrl-H | Toggle hidden files |
| h / ? | Scrollable command table; Esc closes it |
| q / Ctrl-C | Quit; Ctrl-C cancels active work |

Normal letters are commands. Only `/` enters filter text mode, so `dd`, `hjkl`, and spaces typed in the highlighted filter are literal text. `h` is reserved for help; `H`/`u` or Left performs Vim's parent/left action. A `dd` or `gg` sequence must be completed within one second.

Scrolling never changes the active panel. Clicking the inactive panel only focuses it: that click does not move the cursor, mark an item, or open a folder. Inside the active panel, a single click moves the cursor without marking, a double-click opens a folder, and right-click marks/unmarks.

Each panel keeps its own cursor, sorting, marks, and current folder. The selection box shows the active panel's marked paths. Marks persist across directories. Esc in normal mode clears all marks in the active panel, including marks in other folders, while keeping its cursor and query. Esc in an input or confirmation closes that mode first. With no marks, copy, move, and delete use the highlighted file or folder. The direction shown in the toolbar follows the active panel. Scanning, confirmation, and transfer progress stay in a dock below both panels. Copies start immediately unless they total at least 256 MiB or 200 files/folders; those batches show a “may take a while” confirmation with Enter/Click to proceed and Esc to cancel. Errors retain the selection. Ctrl-T remains a copy alias. Permission-denied navigation stays inline.

Use at least 64 columns × 18 rows; 100+ columns gives the file names more room. Narrow panels show Name and Size; Modified appears when space permits. Date/type sorting remain available through Ctrl-O and command-line options.

## In-app options

Press `o` or click Options. Enter/click changes the selected value; Esc returns to the panels. Settings apply to the current session: overwrite policy, preview-only mode, large-copy confirmations, remote-history suggestions, theme, and sorting/direction. Existing shortcuts for paths, hidden files, and machines remain available.

The same screen edits the SSH destination, port, identity file, jump host, and SSH config. Choose Connect/reconnect to apply those fields without quitting. The old connection remains available if the new one fails. If the initial connection fails, Hop stays open with the local panel available so you can correct the connection fields and retry. The machine picker also accepts a typed destination; Enter connects when there are no matching saved hosts, and Ctrl-L uses the typed destination explicitly.

Errors offer Retry and Options. `r` retries the frozen source selection and destination after settings changes; a conflict's Replace choice grants permission for that batch only. File/directory type conflicts, symlink destinations, and missing server atomic-overwrite support still report errors rather than replacing unsafe targets.

## Transfers

Folders copy recursively, including hidden files and empty directories. One global progress display tracks the batch and appends each completed file. Existing files open an in-panel conflict choice: Replace, Cancel, or Options. Replace applies to the current batch only, then preflight runs again. Options can enable replacement for the rest of this session; `--overwrite` still sets the initial policy. Directory contents merge. Linked folders open normally and linked files can be selected. Copies follow source symlinks and copy their target contents under the link name. Recursive symlink loops, broken links, and special files stop preflight before writing. Existing destination symlinks are never overwritten.

```sh
hop my-server --sort date
hop my-server --sort name --order desc
hop my-server --overwrite         # Permit replacing destination files
hop my-server --dry-run           # Preview operations; no copies, moves, deletes or folder creation
hop my-server --yes               # Skip final copy review
```

The engine reuses one SSH/SFTP connection, with up to 16 outstanding 32 KiB chunks within a file. Files are copied sequentially. Many tiny files still incur per-file SFTP round trips; there is no tar streaming or rsync mode.

Moves and deletes always require an in-panel confirmation, including with `--yes`. `m` copies and verifies the entire selection before removing sources; failed copies leave sources in place. Before removal, the original entries and directory contents are checked again. Changed selections stop removal. Deletion is permanent, includes hidden contents of selected directories, and removes symlinks themselves without following their targets. Recursive removal uses individual file and empty-directory operations; it never runs a remote shell `rm` command. A failed or cancelled removal can leave part of the selection already deleted; the error and progress dock report where it stopped.

## Recent machines and paths

Hop discovers SSH destinations from local shell histories, SSH aliases, and recorded connections. When its default settings/history files are absent, it reads the former Hops app's files without modifying them. Ctrl-R combines previous paths with remote history/context suggestions; `--no-history` skips remote shell history. Source/inference labels stay hidden.

```sh
hop hosts
hop config theme lagoon
hop config theme cobalt
hop config theme afterhours
hop demo --theme afterhours
```

Hop settings/state live in `~/Library/Application Support/Hop/` on macOS and `$XDG_CONFIG_HOME/Hop/` (default `~/.config/Hop/`) on Linux. `HOP_HOME` selects an isolated location; the former `HOPS_HOME` remains a fallback alias. `config.json` accepts `{"theme":"lagoon"}`. The command-line theme takes precedence. True-color terminals use the exact palette; Apple Terminal receives a 256-color approximation. `HOP_COLOR_MODE=truecolor` or `HOP_COLOR_MODE=256` overrides detection. `NO_COLOR` is respected.

The inherited `send`, `get`, `ssh`, `shell-init`, and `record` commands remain available. The default `hop` command opens the two-panel workspace. The original single-panel tool is archived separately as `hop-classic`; it is not included in this release.

## Build and validation

Building requires Go 1.24+; there are no third-party Go dependencies.

```sh
make build      # ./hop, with the version from VERSION
make check      # race tests, vet, formatting
make dist       # four release archives and SHA256SUMS; also needs Python 3
make install    # installs to ~/.local/bin; PREFIX can override
```

Full integration tests require Python 3 and a local OpenSSH `sftp-server`. On
Debian/Ubuntu: `sudo apt-get install python3 openssh-sftp-server`. Tests exercise local
SFTP transfers and real pseudo-terminals without connecting to remote machines.

See [RELEASING.md](RELEASING.md) for the tag-triggered GitHub Actions publishing process.

## Migrating from development versions

`hops` is now `hop`. Existing `HOPS_HOME` and `HOPS_COLOR_MODE` overrides still work,
with the corresponding `HOP_*` setting taking priority. Update shell integrations to
use `hop shell-init zsh` (or `bash`).

If both the original single-panel Hop and Hops were installed, preserve the original
Hop config directory and executable separately before installing this version. Copy
the former Hops settings into the canonical Hop directory to retain the two-panel
app's preferences and history. Use a separate `HOP_HOME` for the classic executable.
