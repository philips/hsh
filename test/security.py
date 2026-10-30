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
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
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
    if b"\x1b[c" in chunk:
        os.write(fd, b"\x1b[?62;22c")
    if b"\x1b[?u" in chunk:
        os.write(fd, b"\x1b[?1;0;2004u")
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


def absent(fd, needle, what, timeout=4):
    start = len(transcript)
    end = time.time() + timeout
    while time.time() < end:
        pump(fd)
    if needle in transcript[start:]:
        fail(what)


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


def type_line(fd, line, marker):
    start = len(transcript)
    os.write(fd, b"\x15" + line.encode() + b"\r")
    expect(fd, marker, since=start)


before = pods()
pid, fd = spawn("run", "--pull=never", image)
expect(fd, "# ", 60)
type_line(fd, "PS1='READY> '; echo started-$((6*7))", "started-42")
session = new_pod(before)
expect(fd, "nothing to review", 10)
print("PASS: the session starts with the green status line")

# 1. the agent sprays every query that can make the user terminal reply on
#    the input path; the harness answers as a real terminal would.
mark = len(transcript)
type_line(fd, r"printf '\033[6n\033[c\033[?u\033[=1;1u\033[>1u\033[<u\033[?2004h\033[?2004l\033[?1000h\033[?1006h\033]11;?\a'; echo spray-42", "spray-42")
expect(fd, "spray-42", 10, since=mark)
# 2. the replies themselves (DSR, DA, kitty flags, DECRPM, mouse, focus,
#    paste markers, a ctrl+space release event) arrive on hsh's input.
os.write(fd, b"\x1b[1;1R\x1b[0n\x1b[?62;22c\x1b[?2004;2y\x1b[?1;0;2004u\x1b[I\x1b[O")
os.write(fd, b"\x1b[<0;10;5M\x1b[M !!\x1b[32;5:3u\x1b[32;1u\x1b[32u\x1b[99;5u\x1b[27u")
os.write(fd, b"\x1b[200~\x1b[201~")
absent(fd, "denied hosts awaiting review", "a terminal reply opened the monitor view")
absent(fd, "MONITOR", "a terminal reply painted the monitor status line")
mark = len(transcript)
type_line(fd, "echo alive-$((6*7))", "alive-42")
expect(fd, "nothing to review", 10, since=mark)
print("PASS: agent-provoked terminal queries and replies never open the monitor, and the session repaints an untainted status line")

# 3. a pending request exists; the agent keeps spraying queries and the
#    split-write scroll-region smuggle aimed at the status row.
type_line(fd, "curl -s -m 8 https://blocked.example.test >/dev/null 2>&1; echo curl-done-$((6*7))", "curl-done-42")
expect(fd, "1 proxy request needs review", 20)
mark = len(transcript)
type_line(fd, r"printf '\033[1;1;1;1;1;1;1;1;1'; echo smuggle-42", "smuggle-42")
type_line(fd, r"printf '1;40r'; echo $((6*7))-smuggle-done", "42-smuggle-done")

os.write(fd, b"\x1b[1;1R\x1b[?62;22c\x1b[?1;0;2004u")
absent(fd, "denied hosts awaiting review", "the monitor opened while requests were pending", timeout=4)
if "1 proxy request needs review" not in transcript[mark:]:
    fail("the status line vanished during the agent attack")
print("PASS: with a request pending, agent output cannot open the monitor or take out the status line")

# 4. the genuine key still opens the dialog; the isolation did not break
#    the feature. Widen the terminal first: at 80 columns the URL column
#    truncates the host.
mark = len(transcript)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 120, 0, 0))
os.write(fd, b"\x00")
expect(fd, "denied hosts awaiting review", 10, since=mark)
expect(fd, "https://blocked.example.test", 10, since=mark)
expect(fd, "MONITOR", 10, since=mark)
os.write(fd, b"q")
type_line(fd, "echo after-attack-$((6*7))", "after-attack-42")
print("PASS: the real Ctrl+Space still opens the review and the session survives the attack")

out(hsh, "detach", session[len("hsh-"):])
reap(pid)
out(hsh, "rm", "-f", session)
print("PASS: agent-to-monitor isolation holds end to end")
