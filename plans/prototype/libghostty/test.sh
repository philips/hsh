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

os.write(fd, b"\x1d")
pm("send", "--sock", SOCK, "exit\n")
server.wait(timeout=5)
print("ALL LIBGHOSTTY PROTOTYPE CHECKS PASSED")
PY
