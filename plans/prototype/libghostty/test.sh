#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP="$(mktemp -d)"
trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$TMP"' EXIT
command -v python3 >/dev/null || { echo "SKIP: python3 is needed for the pty driver"; exit 0; }

export GOEXPERIMENT=simd
(cd "$HERE" && go test ./... && go build -o "$TMP/ptymux" .)
export PM="$TMP/ptymux" TMP

python3 - <<'PY'
import os, pty, select, subprocess, sys, time

PM = os.environ["PM"]
TMP = os.environ["TMP"]
SOCK = TMP + "/s.sock"
LOG = TMP + "/decisions"
transcript = ""

def fail(msg):
    print("FAIL:", msg)
    print("--- client transcript ---")
    print(transcript)
    server.kill()
    sys.exit(1)

env = dict(os.environ, PS1="SH$ ")
server = subprocess.Popen([PM, "serve", "--sock", SOCK, "--log", LOG, "--", "bash", "--norc", "--noprofile", "-i"],
                          stdout=subprocess.PIPE, text=True, env=env)
server.stdout.readline()

def pm(*a, **kw):
    return subprocess.run([PM, *a], capture_output=True, text=True, **kw).stdout

def dump():
    return pm("dump", "--sock", SOCK)

def wait_dump(needle, timeout=5):
    end = time.time() + timeout
    while time.time() < end:
        d = dump()
        if needle in d:
            return d
        time.sleep(0.1)
    fail("screen never showed %r; dump:\n%s" % (needle, dump()))

def attach():
    global transcript
    pid, fd = pty.fork()
    if pid == 0:
        os.execv(PM, [PM, "attach", "--sock", SOCK])
    import fcntl, struct, termios
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 100, 0, 0))
    return pid, fd

def pump(fd, timeout=0.2):
    global transcript
    r, _, _ = select.select([fd], [], [], timeout)
    if r:
        try:
            transcript += os.read(fd, 65536).decode(errors="replace")
        except OSError:
            pass

def expect(fd, needle, since, timeout=6):
    end = time.time() + timeout
    while time.time() < end:
        pump(fd)
        if needle in transcript[since:]:
            return
    fail("client never saw %r" % needle)

def settle(fd, t=0.5):
    end = time.time() + t
    while time.time() < end:
        pump(fd, 0.1)

wait_dump("SH$")
pm("send", "--sock", SOCK, "echo persisted-marker\n")
wait_dump("persisted-marker")
print("PASS: the emulator tracks the screen of a session with no client attached")

pid, fd = attach()
expect(fd, "persisted-marker", 0)
print("PASS: attaching restores the existing screen from emulator state")

os.write(fd, b"echo typed-while-attached\r")
expect(fd, "typed-while-attached", 0)
wait_dump("typed-while-attached")
print("PASS: keystrokes and output flow while attached")

os.write(fd, b"\x1d")
expect(fd, "[detached]", 0)
os.waitpid(pid, 0)
os.close(fd)
pm("send", "--sock", SOCK, "echo output-while-detached\n")
wait_dump("output-while-detached")
transcript = ""
pid, fd = attach()
expect(fd, "output-while-detached", 0)
expect(fd, "typed-while-attached", 0)
print("PASS: detach keeps the session running; reattach shows output produced meanwhile")

mark = len(transcript)
notify = subprocess.Popen([PM, "notify", "--sock", SOCK, "Allow example.com?"], stdout=subprocess.PIPE, text=True)
expect(fd, "Allow example.com?", mark)
if "Allow example.com?" in dump():
    fail("the popover leaked into the emulated screen")
os.write(fd, b"y")
out, _ = notify.communicate(timeout=5)
if out.strip() != "approved":
    fail("notify returned %r" % out)
expect(fd, "persisted-marker", mark)
if "SH$ y" in dump():
    fail("the approval keystroke reached the shell")
print("PASS: a popover takes over input, returns the decision, and the screen underneath is restored")

mark = len(transcript)
pm("send", "--sock", SOCK, "printf '\\033]%s;approve;example.org\\a' 7777\n")
expect(fd, "Approve example.org?", mark)
os.write(fd, b"n")
settle(fd)
if not os.path.exists(LOG) or "denied example.org" not in open(LOG).read():
    fail("in-band request was not decided: %r" % (open(LOG).read() if os.path.exists(LOG) else None))
if dump().count("example.org") != 1:
    fail("the custom escape sequence leaked onto the emulated screen")
print("PASS: a custom OSC emitted inside the session raises the popover and never reaches the screen")

def status():
    return pm("status", "--sock", SOCK).strip()

def read_log():
    return open(LOG).read() if os.path.exists(LOG) else ""

GREEN = "\x1b[0;30;42m"
RED = "\x1b[0;97;41m"

if status() != "mode=normal color=green pending=0":
    fail("unexpected idle status: %r" % status())
mark = len(transcript)
pm("deny", "--sock", SOCK, "a.example")
pm("deny", "--sock", SOCK, "b.example")
expect(fd, "2 proxy requests need review", mark)
if GREEN not in transcript[mark:]:
    fail("the status line is not green while requests are pending")
if status() != "mode=normal color=green pending=2":
    fail("unexpected status with two pending: %r" % status())
print("PASS: proxy denials show up in the green status line as a notification")

mark = len(transcript)
os.write(fd, b"\x00")
expect(fd, "Proxy requests awaiting review", mark)
expect(fd, "a.example", mark)
expect(fd, "b.example", mark)
if RED not in transcript[mark:]:
    fail("the status line did not turn red")
if status() != "mode=review color=red pending=2":
    fail("unexpected status in review: %r" % status())
if "Proxy requests awaiting review" in dump():
    fail("the dialog leaked into the emulated screen")
print("PASS: Ctrl+Space turns the status line red and pops over the approval list")

os.write(fd, b"\x1b[B")
os.write(fd, b"a")
settle(fd)
os.write(fd, b"d")
settle(fd)
log = read_log()
if "approved b.example" not in log or "denied a.example" not in log:
    fail("arrow-key selection and a/d decisions were not applied: %r" % log)
if status() != "mode=review color=red pending=0":
    fail("unexpected status after deciding everything: %r" % status())
print("PASS: arrow keys select, a approves, d denies; the status counts what is left")

mark = len(transcript)
os.write(fd, b"\x1b")
expect(fd, "nothing to review", mark)
if status() != "mode=normal color=green pending=0":
    fail("Esc did not close the dialog: %r" % status())
print("PASS: Esc closes the dialog, restores the screen, and the status line is green again")

pm("deny", "--sock", SOCK, "c.example")
settle(fd)
os.write(fd, b"\x00")
settle(fd)
os.write(fd, b"\x00")
settle(fd)
if status() != "mode=normal color=green pending=1":
    fail("Ctrl+Space did not toggle the dialog closed: %r" % status())
os.write(fd, b"\x00")
settle(fd)
os.write(fd, b"A")
settle(fd)
os.write(fd, b"\x00")
settle(fd)
if "approved c.example" not in read_log() or status() != "mode=normal color=green pending=0":
    fail("A did not approve everything pending: %r %r" % (read_log(), status()))
print("PASS: Ctrl+Space toggles the dialog; A approves everything pending")

os.write(fd, b"\x1d")
expect(fd, "[detached]", 0)
os.waitpid(pid, 0)
notify = subprocess.Popen([PM, "notify", "--sock", SOCK, "Queued while detached?"], stdout=subprocess.PIPE, text=True)
time.sleep(0.5)
transcript = ""
pid, fd = attach()
expect(fd, "Queued while detached?", 0)
os.write(fd, b"n")
out, _ = notify.communicate(timeout=5)
if out.strip() != "denied":
    fail("queued notify returned %r" % out)
print("PASS: a prompt raised while nobody is attached waits and appears on the next attach")

res = pm("notify", "--sock", SOCK, "--timeout", "300ms", "Times out?")
if res.strip() != "timeout":
    fail("notify timeout gave %r" % res)
print("PASS: notify times out when unanswered")

mark = len(transcript)
os.write(fd, b"\x1d")
expect(fd, "[detached]", mark)
os.waitpid(pid, 0)
os.close(fd)
pm("send", "--sock", SOCK, "exit\n")
server.wait(timeout=5)

SOCK = TMP + "/p.sock"
server = subprocess.Popen([PM, "serve", "--mode", "passthrough", "--sock", SOCK, "--log", LOG, "--", "bash", "--norc", "--noprofile", "-i"],
                          stdout=subprocess.PIPE, text=True, env=env)
server.stdout.readline()
wait_dump("SH$")
transcript = ""
pid, fd = attach()
expect(fd, "SH$", 0)
if "\x1b[?1049h" in transcript:
    fail("passthrough mode switched the client to the alternate screen")
if "\x1b[1;29r" not in transcript:
    fail("passthrough mode did not reserve the bottom row with a scroll region")
os.write(fd, b"echo raw-forwarded\r")
expect(fd, "raw-forwarded", 0)
print("PASS: passthrough keeps the client on its normal screen, reserves one row, and forwards output")

mark = len(transcript)
os.write(fd, b"\x00")
expect(fd, "Proxy requests awaiting review", mark)
if status() != "mode=review color=red pending=0":
    fail("passthrough review status: %r" % status())
mark = len(transcript)
pm("send", "--sock", SOCK, "seq 1 300\n")
time.sleep(1)
pump(fd, 0.5)
if "300" in transcript[mark:]:
    fail("output reached the client while the dialog was up")
if "300" in dump():
    fail("the emulator advanced while the dialog was up")
os.write(fd, b"\x1b")
expect(fd, "300", mark)
wait_dump("300")
if status() != "mode=normal color=green pending=0":
    fail("passthrough did not return to normal: %r" % status())
print("PASS: while the dialog is up the pty is not read; closing it delivers the held output in order")

os.write(fd, b"\x00")
expect(fd, "nothing to review", len(transcript) - 1)
pm("send", "--sock", SOCK, "seq 1 400\n")
time.sleep(0.5)
mark = len(transcript)
os.write(fd, b"\x1d")
expect(fd, "[detached]", mark)
os.waitpid(pid, 0)
os.close(fd)
wait_dump("400")
if status() != "mode=normal color=green pending=0":
    fail("detaching with the dialog open left the session paused: %r" % status())
print("PASS: detaching while the dialog is open releases the pause so the session keeps running")

pm("send", "--sock", SOCK, "exit\n")
server.wait(timeout=5)
print("ALL LIBGHOSTTY PROTOTYPE CHECKS PASSED")
PY
