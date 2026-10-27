#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP="$(mktemp -d)"
trap 'kill $(jobs -p) 2>/dev/null || true; rm -rf "$TMP"' EXIT
command -v python3 >/dev/null || { echo "SKIP: python3 is needed for the pty driver"; exit 0; }

(cd "$HERE" && go build -o "$TMP/pa" .)
export PA="$TMP/pa" TMP

python3 - <<'PY'
import os, pty, select, subprocess, sys, time

PA = os.environ["PA"]
TMP = os.environ["TMP"]
PROXY = TMP + "/proxy.sock"
ADMIN = TMP + "/admin.sock"
OUT = TMP + "/ticks"

def fail(msg):
    print("FAIL:", msg)
    print("--- transcript ---")
    print(transcript)
    sys.exit(1)

transcript = ""

pid, fd = pty.fork()
if pid == 0:
    os.environ["PS1"] = "SH$ "
    os.execvp("bash", ["bash", "--norc", "--noprofile", "-i"])

def pump(timeout=0.2):
    global transcript
    r, _, _ = select.select([fd], [], [], timeout)
    if r:
        try:
            data = os.read(fd, 65536).decode(errors="replace")
        except OSError:
            return
        transcript += data

def expect(needle, timeout=8, since=None):
    start = len(transcript) if since is None else since
    end = time.time() + timeout
    while time.time() < end:
        pump()
        if needle in transcript[start:]:
            return
    fail("timed out waiting for %r" % needle)

def send(s):
    os.write(fd, s.encode())

expect("SH$ ")

broker = subprocess.Popen([PA, "broker", "--proxy-sock", PROXY, "--admin-sock", ADMIN, "--shell-pid", str(pid)],
                          stdout=subprocess.PIPE, text=True)
broker.stdout.readline()

def ticks():
    try:
        return len(open(OUT).read().splitlines())
    except FileNotFoundError:
        return 0

def run_in_shell(cmd):
    mark = len(transcript)
    send(cmd + "\n")
    expect("SH$ ", since=mark + len(cmd))
    return transcript[mark:]

send("%s guard -- %s agent --out %s --ticks 60\n" % (PA, PA, OUT))
expect("agent: started")
time.sleep(0.5)
before = ticks()
if before == 0:
    fail("the agent is not producing ticks")
print("PASS: guarded agent runs in the foreground job")

mark = len(transcript)
res = subprocess.run([PA, "proxy-deny", "--proxy-sock", PROXY, "example.com"], capture_output=True, text=True)
if "paused" not in res.stdout or "SIGTSTP" not in res.stdout:
    fail("broker did not pause the foreground job with SIGTSTP: %r %r" % (res.stdout, res.stderr))
expect("Stopped", since=mark)
expect("SH$ ", since=mark)
a = ticks()
time.sleep(0.6)
if ticks() != a:
    fail("the agent kept running after it was stopped")
print("PASS: a denial stops the foreground job and returns the user to the shell prompt")

out = run_in_shell("%s pending --admin-sock %s" % (PA, ADMIN))
if "pending example.com" not in out:
    fail("the admin shell cannot list the pending host: %r" % out)
print("PASS: the shell (outside the guard) can list pending approvals")

out = run_in_shell("%s guard -- %s approve --admin-sock %s example.com" % (PA, PA, ADMIN))
if "permission denied" not in out.lower() and "operation not permitted" not in out.lower():
    fail("a guarded process could reach the admin socket: %r" % out)
out = run_in_shell("%s pending --admin-sock %s" % (PA, ADMIN))
if "pending example.com" not in out:
    fail("the guarded approve attempt changed the pending list: %r" % out)
print("PASS: a guarded process cannot connect to the admin socket (Landlock RESOLVE_UNIX)")

out = run_in_shell("%s guard -- sh -c 'kill -9 $PPID; echo survived=$?'" % PA)
if "Operation not permitted" not in out and "not permitted" not in out:
    fail("a guarded process could signal the shell: %r" % out)
run_in_shell("echo shell-alive")
print("PASS: a guarded process cannot signal processes outside its domain (Landlock SCOPE_SIGNAL)")

out = run_in_shell("%s approve --admin-sock %s example.com" % (PA, ADMIN))
if "approved example.com" not in out:
    fail("approval failed: %r" % out)
mark = len(transcript)
send("fg\n")
expect("agent: done", timeout=15, since=mark)
if ticks() != 60:
    fail("the agent did not resume to completion: %d ticks" % ticks())
print("PASS: approving from the shell and running fg resumes the agent to completion")

res = subprocess.run([PA, "proxy-deny", "--proxy-sock", PROXY, "example.com"], capture_output=True, text=True)
if res.stdout.strip() != "allow":
    fail("an approved host is still denied: %r" % res.stdout)
print("PASS: the broker now allows the approved host")

send("exit\n")
broker.terminate()
print("ALL PROCESS-APPROVAL PROTOTYPE CHECKS PASSED")
PY
