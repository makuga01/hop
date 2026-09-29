# Demo media

All MP4s, GIFs, and screenshots show only the terminal viewport. The shell uses
the terminal's plain dark background. Hop's own colors and interface appear
when the program starts, and quitting restores the shell. No branding, frames,
captions, key badges, or other graphics are added to the terminal output.

## Full SSH walkthrough

`hop-walkthrough.mp4`, `hop-walkthrough.gif`, and `hop-walkthrough.cast` capture
the real application from the shell prompt through a successful transfer:

1. Type `hop` and press Enter.
2. Pick `staging-demo` from three fictional SSH aliases.
3. Connect to a real OpenSSH server inside the isolated recording container.
4. Navigate to the remote releases folder.
5. Select and transfer a generated 64 MiB file over SFTP.
6. Inspect the destination, then quit back to the shell.

The recorder checks that the transferred file matches the source byte for byte.
`hop-launch.png`, `hop-server-picker.png`, and `hop-transfer.png` are stills from
this 33-second recording. The server aliases point to container loopback;
the container has no external network, published ports, or host mounts.
Both user accounts, all data, and the SSH keys are created for the recording.

### Recreate the full walkthrough

Install Docker, Go, Python 3, Pillow, pyte 0.8.2, and ffmpeg. Run from the
repository root in a shell:

```sh
recording_build_dir=$(mktemp -d)
case "$(docker info --format '{{.Architecture}}')" in
  aarch64|arm64) recording_arch=arm64 ;;
  x86_64|amd64) recording_arch=amd64 ;;
  *) echo 'Unsupported Docker architecture'; exit 1 ;;
esac
GOOS=linux GOARCH="$recording_arch" CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -ldflags="-s -w -X main.version=$(cat VERSION)" -o "$recording_build_dir/hop" .
cp scripts/recording-fixture/Dockerfile scripts/recording-fixture/start.sh "$recording_build_dir/"
docker build -t hop-recording-fixture:local "$recording_build_dir"
docker run -d --rm --network none --name hop-recording --hostname workstation hop-recording-fixture:local
# Wait until this succeeds before recording:
docker exec --user demo hop-recording ssh staging-demo true
python3 scripts/record-demo.py --container hop-recording
docker stop hop-recording
```

Use a fresh container for each recording so the destination starts empty.
`asciinema play docs/media/hop-walkthrough.cast` replays the raw terminal output.
For Linux, pass `--font` as shown below. Stop the fixture container when finished,
including after a failed recording.

## Offline interface demo

These are recordings of the real Hop 0.1.0 terminal interface. The application
runs in its built-in offline demo, which supplies fictional files, paths, and
the `server (demo)` destination. Copying and remote connections are disabled.

- `hop-demo.mp4`: 29-second H.264 terminal recording.
- `hop-demo.gif`: smaller, looping animated preview.
- `hop-demo.cast`: raw asciicast v2 terminal output, playable with
  `asciinema play docs/media/hop-demo.cast`.
- `hop-workspace.png`: initial two-panel workspace.
- `hop-selection.png`: multiple files selected after filtering.
- `hop-options.png`: session settings with a fictional destination.

The MP4 and GIF render the captured terminal stream with a monospace font.
The renderer preserves the shell and application's separate terminal screens.
No application screens are fabricated. This offline demo does not demonstrate
a real transfer; use the full SSH walkthrough above for that.

### Recreate the offline recording

Install Python 3, Pillow, pyte 0.8.2, and ffmpeg, then run from the repository root:

```sh
make build
python3 scripts/record-demo.py
```

The default font is macOS Menlo. On Linux, pass a monospace font:

```sh
python3 scripts/record-demo.py --font /usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf
```

The capture script starts Hop directly in a fresh temporary working directory.
It passes an explicit environment allowlist, uses an isolated `HOP_HOME`, and
sets `--no-history` and an explicit theme. No shell, desktop, user SSH config,
SSH agent, or private file listing is captured. The asciicast header contains
only dimensions, a generic title, duration, and terminal type.

Before publishing a new recording, inspect the screenshots, play the video, and
review the captured text for unexpected paths, hosts, or personal data.
