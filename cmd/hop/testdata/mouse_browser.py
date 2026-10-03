"""Exercise SGR mouse clicks, wheel, scrollbar and terminal restoration."""
import fcntl
import os
import select
import struct
import subprocess
import sys
import termios
import time

master, slave = os.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 24, 80, 0, 0))
initial = termios.tcgetattr(master)
env = dict(os.environ, HOP_MOUSE_TEST="1", TERM="xterm-256color")
child = subprocess.Popen([sys.argv[1], "-test.run=^TestMouseTerminalHelper$"], stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True)
os.close(slave)
data = bytearray()
cursor = 0

def wait(text):
    global cursor
    target = text.encode()
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        at = data.find(target, cursor)
        if at >= 0:
            cursor = at + len(target)
            return
        if select.select([master], [], [], .1)[0]:
            try: data.extend(os.read(master, 65536))
            except OSError: break
    raise AssertionError(f"Missing {text!r}\n{data.decode(errors='replace')}")

def mouse(button, x, y, release=False):
    if len(sys.argv) > 2 and sys.argv[2] == 'legacy':
        os.write(master, bytes([27, 91, 77, 35 if release else button + 32, x + 32, y + 32]))
    else:
        os.write(master, f"\x1b[<{button};{x};{y}{'m' if release else 'M'}".encode())

def click(button, x, y):
    mouse(button, x, y)
    mouse(button, x, y, True)

try:
    wait("\x1b[?1000h")
    wait("\x1b[?1006h")
    wait("file00.txt")
    click(0, 8, 9)  # left click enters folder
    wait("inside.txt")
    click(0, 8, 8)  # parent
    wait("file00.txt")
    click(0, 8, 11)  # left click marks a file without reviewing
    wait("Selected · 1 item")
    click(2, 8, 9)  # right click marks a directory
    wait("Selected · 2 items")
    click(0, 8, 10)  # permission-denied directory stays in the browser
    wait("Permission denied:")
    # Eleven wheel steps move the cursor from index 2 to 13, revealing file10
    # only after the cursor reaches the bottom of the six-row viewport.
    for _ in range(11):
        mouse(65, 10, 8)
    wait("file10.txt")
    click(0, 77, 13)  # click scrollbar bottom
    wait("file39.txt")
    click(2, 8, 13)  # mark the last file in the scrolled viewport
    wait("Selected · 3 items")
    os.write(master, b"\t")
    wait("Review selected files")
    os.write(master, b"\r")
    wait("\x1b[?1006l")
    wait("MOUSE_OK")
    assert child.wait(timeout=5) == 0, data.decode(errors="replace")
    assert termios.tcgetattr(master) == initial, "terminal left raw"
    print("Mouse PTY: directory/file clicks, right-click marks, denied folder, wheel, scrollbar, cleanup passed.")
finally:
    if child.poll() is None:
        child.kill()
        child.wait(timeout=5)
    os.close(master)
