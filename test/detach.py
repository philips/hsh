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


def reaper_alive(session):
    return session in out("pgrep", "-af", "hsh-reaper --pod")


def type_line(fd, line, marker):
    start = len(transcript)
    os.write(fd, b"\x15" + line.encode() + b"\r")
    expect(fd, marker, since=start)


def hsh_ls():
    return out(hsh, "ls", "--workspace", workdir)


before = pods()
pid, fd = spawn("run", "--pull=never", image)
expect(fd, "# ", 60)
type_line(fd, "PS1='READY> '; X=kept-var; echo started-$((6*7))", "started-42")
pump(fd, 0.5)
session = new_pod(before)
if not session:
    fail("no session pod")
sid = session[len("hsh-"):]
ws = session + "-ws"
secrets = os.path.join(state, "sessions", sid, "secrets.json")
if "Attached" not in hsh_ls():
    fail("hsh ls does not show the attached session: " + hsh_ls())
print("PASS: an attached interactive session is listed as Attached")

type_line(fd, "echo before-$((6*7))", "before-42")
os.write(fd, b"\x10\x11")
time.sleep(0.5)
type_line(fd, "echo still-here-$((6*7))", "still-here-42")
if "detached from session" in transcript:
    fail("podman's Ctrl-P Ctrl-Q still detaches")
print("PASS: podman's default Ctrl-P Ctrl-Q no longer detaches or ends the session")

os.write(fd, b"\x1d")
code = reap(pid)
pump(fd, 0.5)
if code != 0:
    fail("detaching exited %d" % code)
if "detached from session %s" % sid not in transcript:
    fail("no detach message")
if not running(ws):
    fail("the workspace container stopped when the user detached")
if not os.path.exists(secrets):
    fail("secrets.json was scrubbed on detach")
if "Detached" not in hsh_ls():
    fail("hsh ls does not show Detached: " + hsh_ls())
if not reaper_alive(session):
    fail("no reaper is watching the detached session")
print("PASS: Ctrl-] detaches: the shell keeps running, secrets stay, hsh ls shows Detached, a reaper watches it")

pid, fd = spawn("run", "--resume", sid)
transcript_mark = len(transcript)
time.sleep(2)
os.write(fd, b"\x15echo resumed-$X-$((6*7))\r")
expect(fd, "resumed-kept-var-42", 20, since=transcript_mark)
if "Attached" not in hsh_ls():
    fail("hsh ls does not show the resumed session as Attached")
print("PASS: hsh run --resume attaches to the same running shell (its variables survived)")

os.close(fd)
reap(pid)
time.sleep(1)
if not running(ws):
    fail("closing the terminal ended the session")
if "Detached" not in hsh_ls():
    fail("a closed terminal did not leave the session Detached: " + hsh_ls())
print("PASS: closing the terminal detaches instead of killing the session")

def settle(fd, seconds):
    end = time.time() + seconds
    while time.time() < end:
        pump(fd, 0.1)


pid, fd = spawn("run", "--resume", sid)
settle(fd, 3)
type_line(fd, "echo second-attach-$((6*7))", "second-attach-42")
mark = len(transcript)
res = subprocess.run([hsh, "detach", sid], capture_output=True, text=True, cwd=workdir)
if "detached session %s" % sid not in res.stdout:
    fail("hsh detach did not report success: %r %r" % (res.stdout, res.stderr))
code = reap(pid)
pump(fd, 0.5)
if code != 0:
    fail("the force-detached client exited %d" % code)
if "requested by hsh detach" not in transcript[mark:]:
    fail("the force-detached client did not say why it detached")
if not running(ws):
    fail("force-detaching stopped the session")
if "Detached" not in hsh_ls():
    fail("hsh ls does not show the force-detached session as Detached: " + hsh_ls())
again = subprocess.run([hsh, "detach", sid], capture_output=True, text=True, cwd=workdir)
if "is not attached" not in again.stdout:
    fail("detaching an unattached session did not say so: %r" % again.stdout)
print("PASS: hsh detach force-detaches the attached terminal; the session keeps running")

pid_a, fd_a = spawn("run", "--resume", sid)
settle(fd_a, 3)
pid_b, fd_b = spawn("run", "--resume", sid)
mark = len(transcript)
code_b = reap(pid_b)
pump(fd_b, 0.5)
if code_b == 0 or "attached from another terminal" not in transcript[mark:]:
    fail("a second --resume was not refused while the session is attached (exit %d)" % code_b)
pid_c, fd_c = spawn("run", "--resume", "--take-over", sid)
if reap(pid_a) != 0:
    fail("--take-over did not detach the first client cleanly")
settle(fd_c, 3)
type_line(fd_c, "echo took-over-$((6*7))", "took-over-42")
os.write(fd_c, b"\x1d")
reap(pid_c)
if not running(ws):
    fail("--take-over ended the session")
print("PASS: --resume refuses a session attached elsewhere; --resume --take-over detaches it and attaches here")

subprocess.run(["podman", "stop", "-t", "1", ws], capture_output=True)
wait_for(lambda: not os.path.exists(secrets), "the reaper to scrub secrets.json")
wait_for(lambda: "Running" not in out("podman", "pod", "ls", "--filter", "name=" + session, "--format", "{{.Status}}"), "the reaper to stop the pod")
wait_for(lambda: not reaper_alive(session), "the reaper to exit")
print("PASS: when the shell exits while detached the reaper scrubs secrets and stops the pod")

out(hsh, "rm", "-f", session)

before = pods()
pid, fd = spawn("run", "--pull=never", image)
expect(fd, "# ", 60)
type_line(fd, "PS1='READY> '; echo second-$((6*7))", "second-42")
session2 = new_pod(before)
sid2 = session2[len("hsh-"):]
ws2 = session2 + "-ws"
secrets2 = os.path.join(state, "sessions", sid2, "secrets.json")
os.write(fd, b"\x1b[93;5u")
reap(pid)
if not running(ws2):
    fail("the kitty-encoded detach key ended the session instead of detaching")
print("PASS: Ctrl+] sent in the kitty keyboard encoding still detaches (the relay translates it)")
subprocess.run(["pkill", "-f", "hsh-reaper --pod " + session2], capture_output=True)
time.sleep(0.5)
if reaper_alive(session2):
    fail("could not stop the reaper for the gc check")
subprocess.run(["podman", "stop", "-t", "1", ws2], capture_output=True)
time.sleep(1)
if "Shell exited" not in hsh_ls():
    fail("hsh ls does not show Shell exited: " + hsh_ls())
gc = out(hsh, "gc")
if "stopped session %s" % sid2 not in gc:
    fail("gc did not stop the leftover session: " + gc)
if os.path.exists(secrets2):
    fail("gc left secrets.json behind")
print("PASS: with no reaper, hsh ls shows Shell exited and hsh gc stops the pod and scrubs secrets")
out(hsh, "rm", "-f", session2)

before = pods()
pid, fd = spawn("run", "--rm", "--pull=never", image)
expect(fd, "# ", 60)
type_line(fd, "PS1='READY> '; echo temp-$((6*7))", "temp-42")
temp = new_pod(before)
os.write(fd, b"\x1d")
time.sleep(1)
type_line(fd, "echo temp-alive-$((6*7))", "temp-alive-42")
os.write(fd, b"\x15exit\r")
reap(pid)
wait_for(lambda: temp not in pods(), "the --rm session to be removed")
print("PASS: --rm sessions cannot detach and are removed when the shell exits")
