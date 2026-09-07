"""Exercise the actual terminal handoff and one-byte SFTP copy without SSH."""
import errno
import os
import select
import signal
import sys
import subprocess
import termios
import time


def run_once(binary):
    fd, slave = os.openpty()
    initial = termios.tcgetattr(fd)
    env = dict(os.environ, HOP_TERMINAL_TEST="1", TERM="xterm-256color")
    env.pop("NO_COLOR", None)
    child = subprocess.Popen([binary, "-test.run=^TestTerminalHandoffHelper$"], stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True)
    os.close(slave)
    transcript = bytearray()
    cursor = 0

    def wait_for(text):
        nonlocal cursor
        target = text.encode()
        deadline = time.monotonic() + 12
        while time.monotonic() < deadline:
            found = transcript.find(target, cursor)
            if found >= 0:
                cursor = found + len(target)
                return
            if select.select([fd], [], [], 0.1)[0]:
                try:
                    data = os.read(fd, 65536)
                except OSError as exc:
                    if exc.errno == errno.EIO:
                        break
                    raise
                if not data:
                    break
                transcript.extend(data)
        raise AssertionError(f"Missing {text!r}:\n{transcript.decode(errors='replace')}")

    try:
        wait_for("Machine")
        os.write(fd, b"\r")
        wait_for("Upload destination")
        os.write(fd, b"\r")
        wait_for("[Y/n — Enter to copy]")
        # Submit as soon as the prompt appears, while an old reader used to linger.
        os.write(fd, b"y\r")
        wait_for("ONE_BYTE_UPLOAD_OK")
        wait_for("Download source")
        os.write(fd, b"\r")
        wait_for("[Y/n — Enter to copy]")
        os.write(fd, b"\r")
        wait_for("ENTER_DOWNLOAD_OK")
        wait_for("Cancel test?")
        os.write(fd, b"n\r")
        wait_for("Destination path:")
        os.write(fd, b"/tmp/a b\r")
        wait_for("TERMINAL_HANDOFF_OK")
        assert transcript.count(b"Waiting test") >= 3, "heartbeat stopped without byte updates"
        assert b"\x1b[38;2;7;59;54;48;2;154;239;211m" in transcript, "selected row has no color"
        assert b"(inferred)" not in transcript, "source labels remain visible"
        restored = termios.tcgetattr(fd)
        assert restored == initial, "terminal attributes were not restored"
        assert child.wait(timeout=5) == 0, transcript.decode(errors="replace")
    except BaseException:
        try:
            child.kill()
            child.wait(timeout=5)
        except ProcessLookupError:
            pass
        raise
    finally:
        os.close(fd)


for _ in range(3):
    run_once(sys.argv[1])
print("Three PTY runs passed: picker → y confirmation → one-byte copy; Enter confirms; n cancels.")
