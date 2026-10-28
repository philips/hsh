import fcntl
import os
import pty
import select
import struct
import subprocess
import sys
import termios
import time

hsh, workdir, image, state = sys.argv[1:5]
plain_image = sys.argv[5] if len(sys.argv) > 5 else ""
os.chdir(workdir)
transcript = ""


def fail(msg):
    print("FAIL: " + msg)
    print("--- last output ---")
    print(transcript[-1500:])
    sys.exit(1)


def spawn(*args):
    pid, fd = pty.fork()
    if pid == 0:
        os.execv(hsh, [hsh] + list(args))
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 140, 0, 0))
    return pid, fd


def pump(fd, timeout=0.2):
    global transcript
    r, _, _ = select.select([fd], [], [], timeout)
    if not r:
        return
    try:
        chunk = os.read(fd, 65536)
    except OSError:
        return
    if b"\x1b[6n" in chunk:
        os.write(fd, b"\x1b[1;1R")
    if b"\x1b]11;?" in chunk:
        os.write(fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\")
    transcript += chunk.decode(errors="replace")


def expect(fd, needle, timeout=30, since=None):
    start = len(transcript) if since is None else since
    end = time.time() + timeout
    while time.time() < end:
        pump(fd)
        if needle in transcript[start:]:
            return
    fail("timed out waiting for %r" % needle)


def reap(pid, timeout=15):
    end = time.time() + timeout
    while time.time() < end:
        try:
            done, status = os.waitpid(pid, os.WNOHANG)
        except ChildProcessError:
            return 0
        if done:
            return os.waitstatus_to_exitcode(status)
        time.sleep(0.1)
    fail("hsh run did not exit")


def out(*cmd):
    return subprocess.run(list(cmd), capture_output=True, text=True).stdout.strip()


def pods():
    return {l for l in out("podman", "pod", "ls", "--format", "{{.Name}}").splitlines() if l.startswith("hsh-")}


def new_pod(before):
    fresh = pods() - before
    return next(iter(fresh)) if fresh else ""


def running(name):
    return out("podman", "inspect", "--format", "{{.State.Running}}", name) == "true"


def wait_for(cond, what, timeout=20):
    end = time.time() + timeout
    while time.time() < end:
        if cond():
            return
        time.sleep(0.2)
    fail("timed out waiting for " + what)




def type_line(fd, line, marker):
    start = len(transcript)
    os.write(fd, b"\x15" + line.encode() + b"\r")
    expect(fd, marker, since=start)


def read_session_config(sid):
    import json
    with open(os.path.join(state, "sessions", sid, "config.json")) as f:
        return json.load(f)


before = pods()
pid, fd = spawn("run", "--pull=never", image)
expect(fd, "# ", 60)
type_line(fd, "PS1='READY> '; echo started-$((6*7))", "started-42")
session = new_pod(before)
sid = session[len("hsh-"):]
expect(fd, "nothing to review", 10)
print("PASS: the green status line is drawn with the session")

type_line(fd, "curl -s -m 8 https://blocked.example.test >/dev/null 2>&1; echo curl-done-$((6*7))", "curl-done-42")
expect(fd, "1 proxy request needs review", 20)
print("PASS: a proxy denial shows up in the status line")

type_line(fd, "(sleep 2; for i in 1 2 3; do echo late-$i-$((6*7)); done) &", "[1]")
mark = len(transcript)
os.write(fd, b"\x00")
expect(fd, "denied hosts awaiting review", 10, since=mark)
expect(fd, "https://blocked.example.test", 10, since=mark)
expect(fd, "REVIEW", 10, since=mark)
time.sleep(4)
pump(fd, 0.5)
if "late-3-42" in transcript[mark:]:
    fail("session output reached the terminal while the dialog was up")
print("PASS: Ctrl+Space takes over the screen with the hsh monitor view and pauses the session's output")

mark = len(transcript)
os.write(fd, b" ")
time.sleep(0.3)
os.write(fd, b"\r")
expect(fd, "approved 1 host", 10, since=mark)
cfg = read_session_config(sid)
if "blocked.example.test" not in cfg["network"]["allow"]:
    fail("approving did not add the host to the session config: %s" % cfg["network"])
os.write(fd, b"q")
expect(fd, "late-3-42", 10, since=mark)
expect(fd, "nothing to review", 10, since=mark)
print("PASS: selecting with space and approving with enter adds the host to the session config, as in hsh monitor; closing with q delivers the held output and the line goes quiet")

type_line(fd, "echo after-dialog-$((6*7))", "after-dialog-42")
os.write(fd, b"\x1d")
reap(pid)
out(hsh, "rm", "-f", session)
print("PASS: the session stays usable and detaches normally after the dialog")

if plain_image:
    before = pods()
    plain_mark = len(transcript)
    pid, fd = spawn("run", "--pull=never", plain_image)
    expect(fd, "# ", 60)
    type_line(fd, "PS1='READY> '; echo plain-$((6*7))", "plain-42")
    plain_session = new_pod(before)
    os.write(fd, b"\x00")
    type_line(fd, "echo still-plain-$((6*7))", "still-plain-42")
    if "nothing to review" in transcript[plain_mark:] or "Proxy requests awaiting review" in transcript[plain_mark:]:
        fail("monitor.enabled=false still drew the status line or dialog")
    os.write(fd, b"\x1d")
    reap(pid)
    out(hsh, "rm", "-f", plain_session)
    print("PASS: monitor.enabled=false attaches directly with no status line and no Ctrl+Space dialog")
