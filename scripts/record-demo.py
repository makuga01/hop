#!/usr/bin/env python3
"""Record Hop's offline demo or an isolated real SSH walkthrough.

Requires Python 3, Pillow, pyte 0.8.2, ffmpeg, and a monospace TrueType font.
Run `make build` first. For --container, prepare the recording fixture instead.
Only application terminal output is recorded; no desktop capture is used.
"""

import argparse
import codecs
import copy
import fcntl
import json
import os
from pathlib import Path
import select
import shutil
import struct
import subprocess
import tempfile
import termios
import time

from PIL import Image, ImageDraw, ImageFont
import pyte


class TerminalScreen(pyte.Screen):
    """Preserve the shell screen across Hop's alternate-screen session."""

    def set_mode(self, *modes, **kwargs):
        if kwargs.get("private") and 1049 in modes:
            self.primary = copy.deepcopy((self.buffer, self.cursor, self.margins, self.mode))
            self.reset()
        super().set_mode(*modes, **kwargs)

    def reset_mode(self, *modes, **kwargs):
        super().reset_mode(*modes, **kwargs)
        if kwargs.get("private") and 1049 in modes and hasattr(self, "primary"):
            self.buffer, self.cursor, self.margins, self.mode = self.primary
            del self.primary


COLS, ROWS, FPS = 110, 28, 10
CW, CH = 12, 24
DURATION = 29
STEM = "hop-demo"
TITLE = "Hop offline demo with fictional data"
SNAPSHOTS = {16: "hop-workspace.png", 138: "hop-selection.png", 186: "hop-options.png"}
# Seconds, keyboard input, action description, shortcut (for script readers only).
# The recording contains only terminal pixels, without titles or overlays.
STEPS = [
    (0, b"", "Local and remote files, side by side", ""),
    (2, b"j", "Navigate folders with the keyboard", "j"),
    (3, b"\r", "Open the selected folder", "Enter"),
    (5, b"H", "Return to the parent folder", "H"),
    (6, b"/build", "Filter the local file list", "/  build"),
    (7.5, b"\r", "Return focus to the filtered list", "Enter"),
    (8.5, b" ", "Select build.zip", "Space"),
    (9.5, b"\x15", "Selections stay available when the filter changes", "Ctrl-U"),
    (10.5, b"/report", "Find a second file", "/  report"),
    (11.5, b"\r", "Return focus to the file list", "Enter"),
    (12, b" ", "Select report.csv as well", "Space"),
    (13, b"\x15", "Keep both files selected", "Ctrl-U"),
    (14.5, b"\t", "Switch to the remote panel", "Tab"),
    (15.5, b"j", "Browse the fictional remote files", "j"),
    (16.5, b"\r", "Open the remote exports folder", "Enter"),
    (18, b"o", "Change settings without leaving Hop", "o"),
    (19, b"jjjj", "Choose a color theme", "j"),
    (20, b"\r", "Cobalt theme", "Enter"),
    (22, b"\r", "Afterhours theme", "Enter"),
    (24, b"\r", "Back to Lagoon", "Enter"),
    (25, b"\x1b", "Return to the two-panel workspace", "Esc"),
    (26, b"\t", "Ready for the next operation", "Tab"),
]


def capture(binary, output, container=None):
    events = []
    with tempfile.TemporaryDirectory(prefix="hop-demo-") as isolated:
        # An allowlist prevents shell/SSH/history settings from entering the app.
        # Demo dispatch happens before history discovery; its files are built in.
        env = {
            "PATH": "/usr/bin:/bin", "TERM": "xterm-256color",
            "COLORTERM": "truecolor", "HOP_COLOR_MODE": "truecolor",
            "HOP_HOME": isolated, "XDG_CONFIG_HOME": isolated,
            "LANG": "en_US.UTF-8",
        }
        master, slave = os.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))
        command = [str(binary), "demo", "--theme", "lagoon", "--no-history"]
        if container:
            prompt = (
                r"\n  \[\e[38;2;137;180;250m\]~/Projects"
                r"\[\e[0m\]  \[\e[38;2;147;153;178m\]demo@workstation"
                r"\[\e[0m\]\n  \[\e[38;2;166;227;161m\]❯\[\e[0m\] "
            )
            command = [
                "docker", "exec", "-it", "--user", "demo", "--workdir", "/home/demo/Projects",
                "-e", "TERM=xterm-256color", "-e", "COLORTERM=truecolor",
                "-e", "HOP_COLOR_MODE=truecolor", "-e", "HOP_HOME=/home/demo/.config/Hop",
                "-e", "PS1=" + prompt, "-e", "HISTFILE=/dev/null",
                container, "/bin/bash", "--noprofile", "--norc",
            ]
            # Docker needs its ordinary host configuration, but only the explicit
            # variables above enter the isolated container. No host files are mounted.
            env = dict(os.environ)
        process = subprocess.Popen(
            command,
            stdin=slave, stdout=slave, stderr=slave, cwd=isolated,
            env=env, start_new_session=True,
        )
        os.close(slave)
        decoder = codecs.getincrementaldecoder("utf-8")("strict")
        try:
            # Start the recording only once the real shell prompt is complete,
            # so even frame zero contains the prompt rather than startup blankness.
            if container:
                opening = ""
                deadline = time.monotonic() + 10
                while "❯" not in opening:
                    if time.monotonic() >= deadline or process.poll() is not None:
                        raise RuntimeError("The recording shell did not show its prompt")
                    if select.select([master], [], [], 0.05)[0]:
                        opening += decoder.decode(os.read(master, 65536))
                events.append([0, "o", opening])
            start, step = time.monotonic(), 0
            while time.monotonic() - start < DURATION:
                elapsed = time.monotonic() - start
                while step < len(STEPS) and elapsed >= STEPS[step][0]:
                    if STEPS[step][1]:
                        os.write(master, STEPS[step][1])
                    step += 1
                if select.select([master], [], [], 0.02)[0]:
                    text = decoder.decode(os.read(master, 65536))
                    if text:
                        events.append([round(time.monotonic() - start, 4), "o", text])
                if process.poll() is not None:
                    raise RuntimeError("Demo exited before the recording finished")
        finally:
            if process.poll() is None:
                os.write(master, b"\x04" if container else b"\x03")
                try:
                    process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    process.terminate()
                    process.wait(timeout=3)
            os.close(master)
    header = {
        "version": 2, "width": COLS, "height": ROWS, "duration": DURATION,
        "title": TITLE,
        "env": {"TERM": "xterm-256color"},
    }
    (output / f"{STEM}.cast").write_text(
        "\n".join(json.dumps(item, ensure_ascii=False) for item in [header, *events]) + "\n"
    )
    return events


def render(events, output, font_path):
    screen = TerminalScreen(COLS, ROWS)
    stream = pyte.Stream(screen)
    font = ImageFont.truetype(str(font_path), 20)
    try:
        bold_font = ImageFont.truetype(str(font_path), 20, index=1)
    except OSError:
        bold_font = font
    width, height = COLS * CW, ROWS * CH
    colors = {"black": "#000000", "white": "#ffffff"}

    def color(value, default):
        if value == "default":
            return default
        return colors.get(value, "#" + value if len(value) == 6 else value)

    # Stream RGB frames directly to ffmpeg rather than keeping full frames on disk.
    command = [
        "ffmpeg", "-v", "error", "-y", "-f", "rawvideo", "-pix_fmt", "rgb24",
        "-s", f"{width}x{height}", "-r", str(FPS), "-i", "-", "-an",
        "-c:v", "libx264", "-crf", "18", "-preset", "medium", "-pix_fmt", "yuv420p",
        "-movflags", "+faststart", "-map_metadata", "-1", str(output / f"{STEM}.mp4"),
    ]
    encoder = subprocess.Popen(command, stdin=subprocess.PIPE)
    event_index = 0
    try:
        for number in range(DURATION * FPS):
            timestamp = number / FPS
            while event_index < len(events) and events[event_index][0] <= timestamp:
                stream.feed(events[event_index][2])
                event_index += 1
            im = Image.new("RGB", (width, height), "#1e1e1e")
            draw = ImageDraw.Draw(im)
            for y in range(ROWS):
                for x in range(COLS):
                    char = screen.buffer[y][x]
                    fg = color(char.fg, "#e5e5e5")
                    bg = color(char.bg, "#1e1e1e")
                    if char.reverse:
                        fg, bg = bg, fg
                    left, top = x * CW, y * CH
                    draw.rectangle((left, top, left + CW - 1, top + CH - 1), fill=bg)
                    if char.data.strip():
                        draw.text((left, top + 1), char.data, font=bold_font if char.bold else font, fill=fg)
            if not screen.cursor.hidden:
                left, top = screen.cursor.x * CW, screen.cursor.y * CH
                draw.rectangle((left, top + CH - 3, left + CW - 1, top + CH - 2), fill="#e5e5e5")
            if number in SNAPSHOTS:
                im.save(output / SNAPSHOTS[number])
            encoder.stdin.write(im.tobytes())
    finally:
        encoder.stdin.close()
        result = encoder.wait()
    if result:
        raise RuntimeError("ffmpeg failed to encode the recording")
    subprocess.run([
        "ffmpeg", "-v", "error", "-y", "-i", str(output / f"{STEM}.mp4"),
        "-filter_complex",
        "fps=10,scale=1100:-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff[p];"
        "[b][p]paletteuse=dither=bayer:bayer_scale=3:diff_mode=rectangle",
        "-loop", "0", str(output / f"{STEM}.gif"),
    ], check=True)


def main():
    global DURATION, STEM, TITLE, STEPS, SNAPSHOTS
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=Path("docs/media"))
    parser.add_argument("--binary", type=Path, default=Path("hop"))
    parser.add_argument("--font", type=Path, default=Path("/System/Library/Fonts/Menlo.ttc"))
    parser.add_argument("--container", help="Record the shell and real SSH flow in the isolated fixture")
    parser.add_argument("--cast", type=Path, help="Render an existing capture instead of recording again")
    args = parser.parse_args()
    if args.container:
        DURATION, STEM = 21, "hop-walkthrough"
        TITLE = "Hop SSH walkthrough using isolated fictional data"
        SNAPSHOTS = {
            0: "hop-prompt.png", 14: "hop-launch.png", 31: "hop-server-picker.png",
            117: "hop-multiselect.png", 170: "hop-transfer.png",
        }
        STEPS = [
            (0, b"", "Start at the shell prompt", ""),
            (0.8, b"h", "Type hop", "h"),
            (1.0, b"o", "Type hop", "ho"),
            (1.2, b"p", "Type hop", "hop"),
            (1.6, b"\r", "Launch Hop", "Enter"),
            (2.6, b"\x1b[B", "Choose staging-demo from the server list", "Down"),
            (3.5, b"\r", "Connect over SSH", "Enter"),
            (5, b"\t", "Switch to the remote panel", "Tab"),
            (6, b"/releases", "Find the destination folder", "/  releases"),
            (7, b"\r", "Focus the releases folder", "Enter"),
            (7.4, b"\r", "Open the remote destination", "Enter"),
            (8, b"\t", "Switch back to local files", "Tab"),
            (8.4, b"jjj", "Focus config.yaml", "j"),
            (9, b" ", "Select config.yaml", "Space"),
            (9.5, b"j", "Focus release.bin", "j"),
            (10, b" ", "Select release.bin too", "Space"),
            (10.5, b"j", "Focus report.csv", "j"),
            (11, b" ", "Select report.csv as the third file", "Space"),
            (12, b"c", "Copy all three selected files over SFTP", "c"),
            (16, b"\t", "Inspect all three copied files on the server", "Tab"),
            (19, b"q", "Quit Hop and return to the shell", "q"),
        ]
    if not shutil.which("ffmpeg"):
        parser.error("ffmpeg is required")
    if not args.font.exists():
        parser.error("Pass --font with a monospace TrueType font on this system")
    args.output.mkdir(parents=True, exist_ok=True)
    if args.cast:
        events = [json.loads(line) for line in args.cast.read_text().splitlines()][1:]
    else:
        events = capture(args.binary.resolve(), args.output, args.container)
    if args.container and not args.cast:
        for filename in ["config.yaml", "release.bin", "report.csv"]:
            subprocess.run([
                "docker", "exec", args.container, "cmp",
                "/home/demo/Projects/" + filename, "/home/deploy/releases/" + filename,
            ], check=True)
    render(events, args.output, args.font)
    print(f"Saved isolated demo recording and screenshots to {args.output}")


if __name__ == "__main__":
    main()
