# Hop

Copy files between your computer and SSH servers in a two-panel terminal file manager.

[![Launch Hop, choose a server, and copy three files](docs/media/hop-walkthrough.gif)](docs/media/hop-walkthrough.mp4)

Uses your existing SSH config, keys, agent, and jump hosts. Nothing to install on the server.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/makuga01/hop/main/scripts/install.sh | sh
```

Downloads the latest build for your Mac or Linux machine, verifies its checksum, and
installs it to `~/.local/bin/hop`. **No sudo, PATH changes, or shell profile edits.**
If `hop` isn't on your PATH, run `~/.local/bin/hop`.

[Manual download](https://github.com/makuga01/hop/releases/latest) · [Read the installer](scripts/install.sh)

<details>
<summary>Build from source instead (Go 1.24+)</summary>

```sh
git clone https://github.com/makuga01/hop.git
cd hop
make install
~/.local/bin/hop
```

</details>

## Connect

Pick a server from your SSH config or history. Each entry shows its alias and `user@hostname`, so you know where you're connecting.

Already know the destination? Run `hop my-server` or `hop user@hostname`.
To try the interface without connecting, run `hop demo`.

## Copy files

1. Open the destination folder in one panel.
2. Press **Tab** to switch to the source panel.
3. Use the arrow keys and **Space** to select files or folders.
4. Press **c** to copy them across.

Works in either direction. With nothing selected, `c` copies the item under the cursor. Hop asks before replacing existing files.

## Handy keys

| Key | Action |
| --- | --- |
| Enter | Open a folder |
| Backspace | Go up a folder |
| `/` | Filter filenames |
| `P` | Enter a path |
| `b` | Switch servers |
| `o` | Change settings or connection details |
| `h` | Show all shortcuts |
| `q` | Quit |

You can also move with `m` or delete with `D`. Both ask for confirmation; deletion is permanent.

[Full controls and settings](docs/usage.md) · [Build and release notes](docs/releasing.md) · [Watch the demo](docs/media/hop-walkthrough.mp4)

Inspired by [termscp](https://github.com/veeso/termscp).

note:

This is a pure vibed-up slop, just wanted to share it because it made me scp things faster 🤷‍♂️
enjoy
