#!/usr/bin/env python3
"""Record the real offline demo to an asciicast, PNG, GIF, and MP4.

Requires Python 3, Pillow, pyte 0.8.2, ffmpeg, and a monospace TrueType font.
Run `make build` first. No desktop capture or real SSH connections are used.
"""

import argparse
import codecs
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


COLS, ROWS, FPS = 110, 28, 5
CW, CH, TOP, BOTTOM = 12, 24, 62, 78
DURATION = 29
# Seconds, keyboard input, visible explanation, displayed shortcut.
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


def capture(binary, output):
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
        process = subprocess.Popen(
            [str(binary), "demo", "--theme", "lagoon", "--no-history"],
            stdin=slave, stdout=slave, stderr=slave, cwd=isolated,
            env=env, start_new_session=True,
        )
        os.close(slave)
        decoder = codecs.getincrementaldecoder("utf-8")("strict")
        start, step = time.monotonic(), 0
        try:
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
                os.write(master, b"\x03")
                try:
                    process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    process.terminate()
                    process.wait(timeout=3)
            os.close(master)
    header = {
        "version": 2, "width": COLS, "height": ROWS, "duration": DURATION,
        "title": "Hop — offline demo with fictional data",
        "env": {"TERM": "xterm-256color"},
    }
    (output / "hop-demo.cast").write_text(
        "\n".join(json.dumps(item, ensure_ascii=False) for item in [header, *events]) + "\n"
    )
    return events


def render(events, output, font_path):
    screen = pyte.Screen(COLS, ROWS)
    stream = pyte.Stream(screen)
    font = ImageFont.truetype(str(font_path), 20)
    small = ImageFont.truetype(str(font_path), 16)
    width, height = COLS * CW + 48, ROWS * CH + TOP + BOTTOM
    colors = {"default": "#e7fff5", "black": "#000000", "white": "#ffffff"}

    def color(value, default):
        if value == "default":
            return default
        return colors.get(value, "#" + value if len(value) == 6 else value)

    snapshots = {8: "hop-workspace.png", 69: "hop-selection.png", 93: "hop-options.png"}
    # Stream RGB frames directly to ffmpeg rather than keeping full frames on disk.
    command = [
        "ffmpeg", "-v", "error", "-y", "-f", "rawvideo", "-pix_fmt", "rgb24",
        "-s", f"{width}x{height}", "-r", str(FPS), "-i", "-", "-an",
        "-c:v", "libx264", "-crf", "18", "-preset", "medium", "-pix_fmt", "yuv420p",
        "-movflags", "+faststart", "-map_metadata", "-1", str(output / "hop-demo.mp4"),
    ]
    encoder = subprocess.Popen(command, stdin=subprocess.PIPE)
    event_index = 0
    caption = STEPS[0]
    try:
        for number in range(DURATION * FPS):
            timestamp = number / FPS
            while event_index < len(events) and events[event_index][0] <= timestamp:
                stream.feed(events[event_index][2])
                event_index += 1
            caption = max((s for s in STEPS if s[0] <= timestamp), key=lambda s: s[0])
            im = Image.new("RGB", (width, height), "#091e22")
            draw = ImageDraw.Draw(im)
            draw.text((24, 19), "HOP", font=font, fill="#e7fff5")
            draw.text((90, 22), "SSH file manager", font=small, fill="#a3d2c8")
            label = "OFFLINE DEMO / FICTIONAL DATA"
            draw.text((width - 24 - draw.textlength(label, font=small), 22), label,
                      font=small, fill="#ffa888")
            for y in range(ROWS):
                for x in range(COLS):
                    char = screen.buffer[y][x]
                    fg = color(char.fg, "#e7fff5")
                    bg = color(char.bg, "#063537")
                    if char.reverse:
                        fg, bg = bg, fg
                    left, top = 24 + x * CW, TOP + y * CH
                    draw.rectangle((left, top, left + CW - 1, top + CH - 1), fill=bg)
                    if char.data.strip():
                        draw.text((left, top + 1), char.data, font=font, fill=fg)
            draw.text((24, height - 57), caption[2], font=font, fill="#e7fff5")
            draw.text((24, height - 28), "Recorded from Hop 0.1.0. Transfers are disabled in demo mode.",
                      font=small, fill="#a3d2c8")
            if caption[3]:
                text_width = draw.textlength(caption[3], font=font)
                left = width - text_width - 40
                draw.rounded_rectangle((left - 12, height - 61, width - 24, height - 29),
                                       radius=7, fill="#25464a")
                draw.text((left, height - 57), caption[3], font=font, fill="#ffa888")
            if number in snapshots:
                im.save(output / snapshots[number])
            encoder.stdin.write(im.tobytes())
    finally:
        encoder.stdin.close()
        result = encoder.wait()
    if result:
        raise RuntimeError("ffmpeg failed to encode the recording")
    subprocess.run([
        "ffmpeg", "-v", "error", "-y", "-i", str(output / "hop-demo.mp4"),
        "-filter_complex",
        "fps=5,scale=1000:-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff[p];"
        "[b][p]paletteuse=dither=bayer:bayer_scale=3:diff_mode=rectangle",
        "-loop", "0", str(output / "hop-demo.gif"),
    ], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=Path("docs/media"))
    parser.add_argument("--binary", type=Path, default=Path("hop"))
    parser.add_argument("--font", type=Path, default=Path("/System/Library/Fonts/Menlo.ttc"))
    args = parser.parse_args()
    if not shutil.which("ffmpeg"):
        parser.error("ffmpeg is required")
    if not args.font.exists():
        parser.error("Pass --font with a monospace TrueType font on this system")
    args.output.mkdir(parents=True, exist_ok=True)
    events = capture(args.binary.resolve(), args.output)
    render(events, args.output, args.font)
    print(f"Saved isolated demo recording and screenshots to {args.output}")


if __name__ == "__main__":
    main()
