# Hop

Hop is a file manager for macOS and Linux. It uses SSH to connect to a remote computer.
The left panel shows local files. The right panel shows remote files.

Hop finds computers in your SSH configuration and shell history. It is not necessary to enter the SSH address for a computer in the list.
Hop also uses previous transfers and remote shell history to show paths.

Hop uses SFTP through OpenSSH. A Hop installation on the remote computer is not necessary.
The [termscp](https://github.com/veeso/termscp) file manager is the design reference for the panels.

## System requirements

| Item | Requirement |
| --- | --- |
| Local operating system | macOS or Linux |
| Processor | AMD64 or ARM64 |
| Local software | OpenSSH and `stty` |
| Remote software | An SSH server with SFTP |
| Terminal | A minimum of 64 columns and 18 rows |

Go and Python are not necessary to use a release binary.

## Installation

### Select a release file

1. Open the [release page](https://github.com/makuga01/hop/releases).
2. Download the archive for your operating system and processor.
3. Download the `SHA256SUMS` file from the same release.

| Computer | Archive for version 0.1.0 |
| --- | --- |
| macOS with Apple Silicon | `hop_0.1.0_darwin_arm64.tar.gz` |
| macOS with an Intel processor | `hop_0.1.0_darwin_amd64.tar.gz` |
| Linux with an Intel or AMD processor | `hop_0.1.0_linux_amd64.tar.gz` |
| Linux with an ARM64 processor | `hop_0.1.0_linux_arm64.tar.gz` |

### Do a checksum check

The commands in this procedure are for macOS with Apple Silicon.
For a different computer, use its archive name.
On Linux, use `sha256sum` as an alternative to `shasum -a 256`.

1. Enter this command:

   ```sh
   shasum -a 256 hop_0.1.0_darwin_arm64.tar.gz
   ```

2. Compare the result with the value for that archive in `SHA256SUMS`.
3. If the values are different, stop the installation.

### Install Hop

1. Extract the archive:

   ```sh
   tar -xzf hop_0.1.0_darwin_arm64.tar.gz
   ```

2. Make the installation folder:

   ```sh
   mkdir -p ~/.local/bin
   ```

3. Install the binary:

   ```sh
   install -m 755 hop ~/.local/bin/hop
   ```

4. If necessary, add this line to your shell configuration file:

   ```sh
   export PATH="$HOME/.local/bin:$PATH"
   ```

5. Open a new terminal.
6. Show the installed version:

   ```sh
   hop --version
   ```

## Start Hop

To select a computer from the computer list, enter this command:

```sh
hop
```

To connect with an SSH alias, enter this command:

```sh
hop my-server
```

To use the last computer, enter this command:

```sh
hop --last
```

To set the first local and remote folders, enter this command:

```sh
hop my-server --to ~/Projects --path /srv/app
```

To start the demonstration mode, enter this command:

```sh
hop demo
```

The `demo` command does not connect to a remote computer or change files.
Hop uses your SSH configuration, SSH agent, keys, and jump hosts for connections.

## File transfer

The active panel supplies the source files. The other panel supplies the destination folder.
The cursor identifies one item. A selection can contain many files and folders.

### Copy files or folders

1. Open the destination folder in one panel.
2. Press `Tab` to activate the source panel.
3. Move the cursor to a source item.
4. Press `Space` to select the item.
5. For each other item, do steps 3 and 4 again.
6. Press `c` to copy the selection.

The selection box shows the selected paths. `Space` does not move the cursor.
Selections stay available when you open a different folder.
Without a selection, Hop uses the item at the cursor.

The two panels stay on the screen during a transfer. The progress area shows the full transfer and each completed file.
An approval step is necessary for transfers of 256 MiB or more, or 200 items or more.
The `--yes` option removes this approval step for copies.

### Replace files

If the destination contains a file with the same name, Hop shows the replacement options below the panels.
Select `Replace` to replace files for this transfer.
Select `Options` to change the replacement setting for the session.
Select `Cancel` to stop the transfer.

Hop copies source contents into destination folders.
It stops if the file types are different or the destination is a symbolic link.

### Move files

1. Select the source files or folders.
2. Press `m`.
3. Examine the source and destination paths in the approval area.
4. Give your approval for the move.

Hop copies the full selection before it removes the source files.
If Hop cannot complete a copy, it keeps the source files.
Hop also stops removal if the source files changed after the initial scan.

### Delete files

**CAUTION: HOP CANNOT RESTORE DELETED FILES. MAKE A BACKUP BEFORE YOU DELETE FILES.**

1. Activate the applicable panel.
2. Select the files or folders.
3. Press `D`.
4. Examine the selected paths in the approval area.
5. Give your approval for the deletion.

Hop deletes all contents of selected folders. This includes hidden files.
For a symbolic link, Hop deletes the link. It does not delete the link target.
If deletion stops, some files can be missing.
Moves and deletions always have an approval step. The `--yes` option does not remove this step.

## Keyboard controls

These commands apply when the filter and other input fields are not active.

| Key | Function |
| --- | --- |
| `Tab` | Activate the other panel. |
| `j` or Down | Move the cursor down. |
| `k` or Up | Move the cursor up. |
| `l`, Right, or Enter | Open the folder at the cursor. |
| Enter on a file | Select or deselect the file. |
| `H`, `u`, Left, or Backspace | Open the parent folder. |
| `gg` or Home | Move the cursor to the first item. |
| `G` or End | Move the cursor to the last item. |
| Space | Select or deselect the item at the cursor. |
| Esc | Deselect all items in the active panel. |
| `c`, `y`, Ctrl-S, or Ctrl-T | Copy to the other panel. |
| `m` | Move to the other panel. |
| `dd` or `D` | Delete the source selection. |
| `n` or Ctrl-N | Make a folder. |
| `P` or Ctrl-L | Enter a path. |
| `b` or Ctrl-B | Select a different computer. |
| `r` or Ctrl-R | Open the list of previous paths. |
| `R` or Ctrl-E | Read the contents of the two folders again. |
| `s` or Ctrl-O | Change the sort order. |
| `.` or Ctrl-H | Show or hide hidden files. |
| `/` | Activate the filter field. |
| Ctrl-U | Clear the filter. |
| `o` | Open the options. |
| `h` or `?` | Show the full command table. |
| `q` | Stop Hop. |
| Ctrl-C | Stop the active operation, or stop Hop if no operation is active. |

Enter each `dd` or `gg` sequence in one second.
In an input field or approval area, Esc closes that area first.
If no items are selected, Esc clears the filter or status message.

## Mouse controls

A click in the other panel activates that panel. That click does not move the cursor or select an item.

| Mouse input in the active panel | Result |
| --- | --- |
| One left click on an item | The cursor moves to the item. |
| Two left clicks on a folder | The folder opens. |
| One right click on an item | The selection state of the item changes. |
| A click on a column heading | The sort order changes. |
| A drag of the scroll bar | A different part of the list shows on the screen. |

The mouse wheel moves the cursor in the active panel. The position of the mouse pointer does not change the active panel.

## Filters and options

To enter filter text, press `/`. The filter field changes color to show that it is active.
To activate the file list, press Esc or Enter. The filter text stays in the field.

Hop hides hidden files at the start. To show them, press `.` or Ctrl-H.

To change the session settings, press `o`. The options include replacement, transfer approval, remote history, colors, and sort order.
The same area contains the SSH address, port, key file, jump host, and configuration file.
You can change these values without a restart.

If there is a connection or transfer error, the panels stay open.
The error area gives access to `Retry` and `Options`.

## Transfer information

Hop copies folders and their contents. This includes hidden files and empty folders.
It follows source symbolic links to files and folders.
The copy uses the link name and the target contents.
Before a transfer, Hop examines the source selection for broken links, link loops, and special files.

Hop uses one SSH connection for a transfer. It copies files one at a time.
For each file, it can send 16 requests at the same time. Each request contains a maximum of 32 KiB.
Hop sends SFTP requests for each file. Transfers of many small files can be slow.
Hop does not use `tar` or `rsync` for transfers.

The `--dry-run` option shows the operation. In this mode, Hop does not copy, move, or delete files or make folders.

## History and settings

Hop reads SSH aliases, local shell history, and connection records to make the computer list.
The path list also uses previous transfers and remote shell history.
The `--no-history` option prevents access to remote shell history.

To show the computer list in the terminal, enter this command:

```sh
hop hosts
```

| Operating system | Settings folder |
| --- | --- |
| macOS | `~/Library/Application Support/Hop/` |
| Linux | `$XDG_CONFIG_HOME/Hop/` |
| Linux without `XDG_CONFIG_HOME` | `~/.config/Hop/` |

The `HOP_HOME` variable sets a different settings folder.
Hop stores settings in `config.json` and history in `state.json`.

### Colors

The available themes are `lagoon`, `cobalt`, and `afterhours`.
The default theme is `lagoon`.

To save a different theme, enter this command:

```sh
hop config theme cobalt
```

To change the theme for one session, enter this command:

```sh
hop --theme afterhours
```

Hop uses true color when the terminal has this function.
Apple Terminal uses the 256-color mode.
The `HOP_COLOR_MODE` variable sets the color mode to `truecolor` or `256`.
The `NO_COLOR` variable disables colors.

### Previous versions

The two-panel program name changed from `hops` to `hop`.
If the new settings files are missing, Hop reads the previous files from the `Hops` settings folder.
The `HOPS_HOME` and `HOPS_COLOR_MODE` variables are also available.
Hop uses the `HOP_*` value if you set the two variables for the same function.

The previous single-panel program is not in this release.
Its archive name is `hop-classic`.
For that program, use a different `HOP_HOME` folder.

## Compile the source files

Go 1.24 is the minimum version for source compilation.
Python 3 and the OpenSSH `sftp-server` program are also necessary for the full test procedure.
On Debian or Ubuntu, the package names are `python3` and `openssh-sftp-server`.

1. Get the source files:

   ```sh
   git clone https://github.com/makuga01/hop.git
   ```

2. Open the source folder:

   ```sh
   cd hop
   ```

3. Compile the source files:

   ```sh
   make build
   ```

4. Do the tests:

   ```sh
   make check
   ```

5. Install the binary:

   ```sh
   make install
   ```

The tests use local SFTP processes and pseudo-terminals. The tests do not connect to remote computers.
The `make dist` command makes four release archives and a checksum file.
GitHub Actions publishes the release files when you push a version tag.
For the release procedure, refer to [RELEASING.md](RELEASING.md).
