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
The installer puts `hop` in `~/.local/bin`. To keep it on your PATH, add
`export PATH="$HOME/.local/bin:$PATH"` to your shell startup file.

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

When filenames conflict, choose **Replace** for that transfer, **Options** to change
the session's overwrite setting, or **Cancel**. Folder contents merge.
Copies of at least 256 MiB or 200 items ask for confirmation before starting.

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

Transfers use one SSH/SFTP connection and copy files sequentially. Many small files
can be slow because each requires separate SFTP requests.
If a connection or transfer fails, the panels stay open with Retry and Options available.

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
hop config theme cobalt    # Save a default theme
hop --theme afterhours     # Choose a theme for this session
hop my-server --sort date
hop my-server --sort name --order desc
```

Themes: `lagoon` (default), `cobalt`, and `afterhours`.
Hop uses true color when available, with a 256-color fallback for Apple Terminal.
Set `HOP_COLOR_MODE` to `truecolor` or `256` to override detection. `NO_COLOR` disables colors.

### Upgrading from Hops

If Hop's settings files don't exist, it reads the former Hops settings.
`HOPS_HOME` and `HOPS_COLOR_MODE` still work; the `HOP_*` values take precedence.
Keep a separate `HOP_HOME` if you also use the older single-panel `hop-classic`.

## Development

```sh
make build   # Build ./hop
make check   # Race tests, vet, and formatting checks
make dist    # Build archives for macOS/Linux on AMD64/ARM64
```

Tests need Python 3 and a local OpenSSH SFTP server. On Debian/Ubuntu, install
`python3` and `openssh-sftp-server`. Tests use local processes and pseudo-terminals.
See [RELEASING.md](../RELEASING.md) for publishing and [the media guide](media/README.md)
for recreating the demo recordings.
