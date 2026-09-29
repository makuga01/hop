# Demo media

These are recordings of the real Hop 0.1.0 terminal interface. The application
runs in its built-in offline demo, which supplies fictional files, paths, and
the `server (demo)` destination. Copying and remote connections are disabled.

- `hop-demo.mp4`: 29-second H.264 recording with explanatory captions.
- `hop-demo.gif`: smaller, looping animated preview.
- `hop-demo.cast`: raw asciicast v2 terminal output, playable with
  `asciinema play docs/media/hop-demo.cast`.
- `hop-workspace.png`: initial two-panel workspace.
- `hop-selection.png`: multiple files selected after filtering.
- `hop-options.png`: session settings with a fictional destination.

The MP4 and GIF render the captured terminal stream with a monospace font.
The title and explanatory captions are added outside the application viewport.
No application screens are fabricated. The demo does not demonstrate a real
transfer; its disabled transfer behavior is labeled in the video.

## Recreate the recording

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
