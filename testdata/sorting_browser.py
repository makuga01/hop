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
env = dict(os.environ, HOP_SORT_TEST="1", TERM="xterm-256color")
child = subprocess.Popen([sys.argv[1], "-test.run=^TestSortingTerminalHelper$"], stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True)
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
    os.write(master, f"\x1b[<{button};{x};{y}{'m' if release else 'M'}".encode())

def click(button, x, y):
    mouse(button, x, y)
    mouse(button, x, y, True)

try:
    wait("alpha.txt")
    click(0, 70, 6)
    wait("Modified ↓")
    click(0, 8, 10)  # parent + folder stay first; newest file follows
    wait("Selected · 1 item")
    wait("gamma.csv")
    click(0, 70, 6)
    wait("Modified ↑")
    click(0, 8, 10)
    wait("Selected · 2 items")
    click(0, 70, 5)  # sort control
    wait("Sort files")
    click(0, 8, 9)  # size, largest first
    wait("Size ↓")
    click(0, 52, 6)  # reverse size, smallest first
    wait("Size ↑")
    click(0, 8, 10)
    wait("Selected · 3 items")
    os.write(master, b"\t")
    wait("Review selected files")
    os.write(master, b"\r")
    wait("SORT_OK")
    assert child.wait(timeout=5) == 0, data.decode(errors="replace")
    assert termios.tcgetattr(master) == initial, "terminal left raw"
    print("Sorting PTY: clickable date/size headers, direction reversal, sort menu and file selection passed.")

finally:
    if child.poll() is None:
        child.kill()
        child.wait(timeout=5)
    os.close(master)
