#!/usr/bin/env python3
import fcntl
import os
import pty
import re
import select
import struct
import sys
import termios
import time

spike = os.path.join(os.environ.get("SPIKE_DIR", os.path.expanduser("~/.cache/gvx-spike")), "spike")
image = sys.argv[1] if len(sys.argv) > 1 else "ghcr.io/philips/hsh-claude-code:latest"


def set_size(fd, rows, cols):
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))


pid, fd = pty.fork()
if pid == 0:
    os.execv(spike, [spike, "-pull", "never", "-tty", "-image", image, "--", "bash", "--norc", "-i"])

set_size(fd, 40, 120)
buf = b""


def read_until(pattern, timeout=15):
    global buf
    deadline = time.time() + timeout
    while time.time() < deadline:
        m = re.search(pattern, buf)
        if m:
            out, buf = buf[: m.end()], buf[m.end():]
            return out
        r, _, _ = select.select([fd], [], [], 0.2)
        if r:
            try:
                buf += os.read(fd, 65536)
            except OSError:
                break
    raise SystemExit(f"timeout waiting for {pattern!r}; got {buf[-500:]!r}")


def query(expr, tag, timeout=15):
    os.write(fd, f'echo "{tag}=[{expr}]"\n'.encode())
    m = read_until(tag.encode() + rb"=\[([^\]\r\n$]*)\]", timeout)
    return re.search(tag.encode() + rb"=\[([^\]\r\n$]*)\]", m).group(1).decode(errors="replace")


class Results(dict):
    def __setitem__(self, k, v):
        print(f"{k}: {v}", flush=True)
        super().__setitem__(k, v)


results = Results()
read_until(rb"[#$] ")
results["tty"] = query("$(tty 2>&1)", "TTY")
results["initial size (want 40 120)"] = query("$(stty size)", "SIZE1")
set_size(fd, 50, 160)
time.sleep(0.5)
results["after resize (want 50 160)"] = query("$(stty size)", "SIZE2")
os.write(fd, b"trap 'echo GOT_WINCH' WINCH; sleep 3 & wait\n")
time.sleep(0.5)
set_size(fd, 30, 100)
try:
    read_until(rb"GOT_WINCH\r", timeout=6)
    results["SIGWINCH to shell"] = "delivered"
except SystemExit:
    results["SIGWINCH to shell"] = "NOT delivered"
os.write(fd, b"trap - WINCH\n")
os.write(fd, b"sleep 30\n")
time.sleep(1)
os.write(fd, b"\x1a")
try:
    read_until(rb"Stopped", timeout=5)
    results["ctrl-z / jobs"] = query("$(jobs)", "JOBS")
    os.write(fd, b"kill -9 %+; wait 2>/dev/null\n")
    results["jobs after kill -9"] = query("$(jobs)", "JOBS2")
except SystemExit:
    results["ctrl-z / jobs"] = "ctrl-z did not stop the job"
os.write(fd, b"sleep 30\n")
time.sleep(1)
start = time.time()
os.write(fd, b"\x03")
results["ctrl-c"] = "interrupted" if query("after", "CTRLC", timeout=5) == "after" and time.time() - start < 5 else "not interrupted"
results["/dev/tty"] = query("$(ls -l /dev/tty 2>&1 | cut -c1-40); $(readlink /proc/self/fd/0)", "DEVTTY")
results["TERM"] = query("$TERM", "TERM")
os.write(fd, b"exit 7\n")
deadline = time.time() + 10
status = None
while time.time() < deadline:
    r, _, _ = select.select([fd], [], [], 0.2)
    if r:
        try:
            tail = os.read(fd, 65536)
            pass
        except OSError as e:
            time.sleep(0.5)
    done, st = os.waitpid(pid, os.WNOHANG)
    if done:
        status = os.waitstatus_to_exitcode(st)
        break
results["exit status (want 7)"] = status if status is not None else "did not exit within 10s"
if status is None:
    os.kill(pid, 9)
