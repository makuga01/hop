"""Mark remote files and a folder, recover from permission denial, review, copy."""
import os
import select
import subprocess
import sys
import termios
import time

master, slave = os.openpty()
initial = termios.tcgetattr(master)
env = dict(os.environ, HOP_BROWSER_TEST="1", TERM="xterm-256color")
env.pop("NO_COLOR", None)
child = subprocess.Popen([sys.argv[1], "-test.run=^TestFileBrowserTerminalHelper$"], stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True)
os.close(slave)
data = bytearray()
cursor = 0


def wait(text):
    global cursor
    target = text.encode()
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        at = data.find(target, cursor)
        if at >= 0:
            cursor = at + len(target)
            return
        if select.select([master], [], [], .1)[0]:
            try:
                data.extend(os.read(master, 65536))
            except OSError:
                break
    raise AssertionError(f"Missing {text!r}\n{data.decode(errors='replace')}")


try:
    wait("alpha.txt")
    os.write(master, b"alpha ")
    wait("1 marked")
    # Space twice must unmark the same item, with no cursor advance.
    os.write(master, b" ")
    wait("0 marked")
    os.write(master, b" ")
    wait("1 marked")
    wait("Selected · 1 item")
    wait("alpha.txt")
    os.write(master, b"\x12")
    wait("Recent locations")
    wait("Selected · 1 item")
    wait("alpha.txt")
    os.write(master, b"\r")
    # The selection box also contains alpha while the directory is loading.
    # Wait for an entry that appears only in the completed listing.
    wait("private/")
    wait("alpha.txt")
    os.write(master, b"\x15private\r")
    wait("Permission denied:")
    # Still in the same listing, with alpha marked. No reconnect/restart.
    os.write(master, b"\x15beta ")
    wait("2 marked")
    os.write(master, b"\x15bundle ")
    wait("3 marked")
    wait("Selected · 3 items")
    wait("bundle/")
    os.write(master, b"\t")
    wait("Review selected files")
    os.write(master, b"\x1b")
    wait("BROWSER_TEST")
    wait("3 marked")
    os.write(master, b"\t")
    wait("Review selected files")
    os.write(master, b"\r")
    wait("Connecting to test server")
    wait("AUTH_TEST > ")
    # Leave time for the heartbeat to redraw without consuming prompt input.
    time.sleep(.3)
    os.write(master, b"ready\n")
    wait("DESTINATION_TEST")
    wait("Selected · 3 items")
    wait("bundle/")
    wait("Use folder")
    # Wait until the initial directory listing is ready before choosing it.
    wait("Right-click Mark")
    os.write(master, b"\x0e")
    wait("New folder:")
    os.write(master, b"existing\r")
    wait("Cannot create folder:")
    os.write(master, b"\x15new folder\r")
    wait("Creating folder")
    wait("Folder created")
    os.write(master, b"\t")
    wait("BROWSER_BATCH_OK")
    wait("FULLSCREEN_CLOSED")
    batch_start = data.index("Batch transfer".encode())
    batch_output = data[batch_start:data.index(b"BROWSER_BATCH_OK")]
    assert b"\x1b[2J" not in batch_output, "batch cleared the screen between files"
    assert b"3/3 files" in batch_output, "global file count missing"
    for name in (b"alpha.txt", b"beta.txt", b"bundle/nested.txt"):
        assert name in batch_output, "completed file missing from batch log"
    assert data.count(b"\x1b[?1049h") == 1, "fullscreen entered more than once"
    assert data.count(b"\x1b[?1049l") == 1, "fullscreen exited between views"
    assert child.wait(timeout=5) == 0, data.decode(errors="replace")
    assert termios.tcgetattr(master) == initial, "terminal left raw"
    print("Single-pane PTY: mark, recent locations, denied folder, recover, Tab review/back and destination, files and recursive folder download passed.")
finally:
    if child.poll() is None:
        child.kill()
        child.wait(timeout=5)
    os.close(master)
