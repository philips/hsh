#!/usr/bin/env python3
import argparse
import hashlib
import os
import random
import re
import sys

DIRECTIVE = re.compile(r"<!--\s*tape:(?P<cmd>[a-zA-Z]+)(?P<args>.*?)-->")
REGION = re.compile(r"<!--\s*tape\s+(?P<edge>START|END)\s*-->")
FENCE = re.compile(r"^```")


def parse_kv(args):
    flags = set()
    kv = {}
    for tok in args.strip().split():
        if "=" in tok:
            k, v = tok.split("=", 1)
            kv[k.lower()] = v
        else:
            flags.add(tok.lower())
    return flags, kv


def esc(s):
    s = s.replace('"', "'")
    return s.replace("\\", "\\\\")


def split_wait(args):
    toks = args.split()
    timeout = "60s"
    cmd = []
    for t in toks:
        if t.startswith("timeout="):
            timeout = t.split("=", 1)[1]
        else:
            cmd.append(t)
    return " ".join(cmd), timeout


def wait_prefix(args, kv):
    cmd, timeout = split_wait(args)
    if not cmd:
        return None
    tries = max(1, int(parse_ms(timeout, 60000) / 500))
    return f'i=0; until {cmd} >/dev/null 2>&1; do i=$((i+1)); [ $i -ge {tries} ] && break; sleep 0.5; done; '


def parse_ms(s, default=60.0):
    s = str(s).strip().lower()
    try:
        if s.endswith("ms"):
            return float(s[:-2])
        if s.endswith("s"):
            return float(s[:-1]) * 1000
        return float(s)
    except ValueError:
        return default


def humanize(cmdline, base_ms):
    rng = random.Random(int(hashlib.md5(cmdline.encode()).hexdigest(), 16))
    out = []
    words = cmdline.split(" ")
    n = len(words)
    for i, word in enumerate(words):
        seg = word + (" " if i < n - 1 else "")
        spd = max(25, base_ms * rng.uniform(0.65, 1.45))
        at = f"{int(round(spd))}ms"
        if len(word) >= 3 and rng.random() < 0.07:
            stray = rng.choice("asdfghjklqwertyuiop")
            out.append(f'Type@{at} "{esc(stray)}"')
            out.append(f"Sleep {rng.randint(110, 240)}ms")
            out.append("Backspace")
        out.append(f'Type@{at} "{esc(seg)}"')
        r = rng.random()
        if r < 0.08:
            out.append(f"Sleep {rng.randint(300, 650)}ms")
        elif r < 0.33:
            out.append(f"Sleep {rng.randint(70, 190)}ms")
    return out


def convert(text, human=False):
    lines = text.splitlines()
    out = []
    i = 0
    n = len(lines)
    have_region = any(REGION.search(l) for l in lines)
    active = not have_region
    pending_run = None
    pending_wait = None

    def emit(s):
        out.append(s)

    while i < n:
        line = lines[i]

        r = REGION.search(line)
        if r:
            active = r.group("edge") == "START"
            i += 1
            continue

        m = DIRECTIVE.search(line)
        if m:
            cmd = m.group("cmd").lower()
            flags, kv = parse_kv(m.group("args"))
            config_cmds = {"output", "set", "require"}
            if not active and cmd not in config_cmds:
                i += 1
                continue

            if pending_wait and cmd != "run":
                w = pending_wait
                pending_wait = None
                emit(f'Type "{esc(w)}"')
                emit("Enter")
                emit("Sleep 500ms")

            if cmd == "output":
                path = m.group("args").strip().strip('"')
                emit(f'Output "{path}"')
            elif cmd == "set":
                emit(f'Set {m.group("args").strip()}')
            elif cmd == "require":
                emit(f'Require {m.group("args").strip()}')
            elif cmd == "sleep":
                emit(f'Sleep {m.group("args").strip()}')
            elif cmd == "key":
                emit(m.group("args").strip())
            elif cmd == "type":
                emit(f'Type "{esc(m.group("args").strip().strip(chr(34)))}"')
            elif cmd == "monitor":
                pre = kv.get("sleep", "1s")
                if pre.lower() != "none":
                    emit(f"Sleep {pre}")
                emit("Ctrl+Space")
                post = kv.get("after", "1s")
                if post.lower() != "none":
                    emit(f"Sleep {post}")
            elif cmd == "hide":
                emit("Hide")
            elif cmd == "show":
                emit("Show")
            elif cmd == "wait":
                pending_wait = wait_prefix(m.group("args"), kv)
            elif cmd == "raw":
                emit(m.group("args").strip())
            elif cmd == "say":
                pass
            elif cmd == "run":
                pending_run = (flags, kv)
            i += 1
            continue

        if not active:
            i += 1
            continue

        if pending_run is not None and FENCE.match(line):
            flags, kv = pending_run
            pending_run = None
            i += 1
            block = []
            while i < n and not FENCE.match(lines[i]):
                block.append(lines[i])
                i += 1
            i += 1

            if pending_wait:
                block[0] = pending_wait + block[0]
                pending_wait = None

            speed = kv.get("typingspeed")
            human_this = "human" in flags or (human and "nohuman" not in flags)
            if speed and not human_this:
                emit(f"Set TypingSpeed {speed}")
            base_ms = parse_ms(speed, 60.0) if speed else 60.0
            for cmdline in block:
                if not cmdline.strip():
                    continue
                if human_this:
                    for c in humanize(cmdline, base_ms):
                        emit(c)
                else:
                    emit(f'Type "{esc(cmdline)}"')
                if "noenter" not in flags:
                    emit("Enter")
            if speed and not human_this:
                emit("Set TypingSpeed 60ms")
            sleep = kv.get("sleep", "2s")
            if sleep.lower() != "none":
                emit(f"Sleep {sleep}")
            continue

        i += 1

    return "\n".join(out) + "\n"


def extract_script(text):
    lines = text.splitlines()
    out = ["#!/usr/bin/env bash", "# extracted from annotated markdown for live debugging", ""]
    i, n = 0, len(lines)
    have_region = any(REGION.search(l) for l in lines)
    active = not have_region
    want_block = False
    for_run = False
    while i < n:
        line = lines[i]
        r = REGION.search(line)
        if r:
            active = r.group("edge") == "START"
            i += 1
            continue
        m = DIRECTIVE.search(line)
        if m and active:
            c = m.group("cmd").lower()
            if c == "run":
                want_block = True
            elif c == "wait":
                wcmd, timeout = split_wait(m.group("args"))
                if wcmd:
                    tries = max(1, int(parse_ms(timeout, 60000) / 500))
                    out.append(f'i=0; until {wcmd} >/dev/null 2>&1; do i=$((i+1)); [ "$i" -ge {tries} ] && exit 1; sleep 0.5; done')
                    out.append("")
            i += 1
            continue
        if active and want_block and FENCE.match(line):
            want_block = False
            i += 1
            while i < n and not FENCE.match(lines[i]):
                if lines[i].strip():
                    out.append(lines[i])
                i += 1
            i += 1
            continue
        i += 1
    return "\n".join(out) + "\n"


DUR = re.compile(r"^(Sleep\s+)(\d+(?:\.\d+)?)(ms|s)\b", re.IGNORECASE)


def scale_sleeps(tape, factor):
    def repl(m):
        val = float(m.group(2))
        unit = m.group(3)
        ms = val * (1000 if unit.lower() == "s" else 1)
        ms = max(50, ms / factor)
        return f"{m.group(1)}{int(round(ms))}ms"
    return "\n".join(DUR.sub(repl, l) for l in tape.splitlines()) + "\n"


def apply_fast(tape, framerate, scale):
    lines = tape.splitlines()
    out = []
    have_fr = any(l.lower().startswith("set framerate") for l in lines)
    inserted = False
    for l in lines:
        low = l.lower()
        if low.startswith("set width ") or low.startswith("set height "):
            parts = l.split()
            try:
                parts[2] = str(int(int(parts[2]) * scale))
            except ValueError:
                pass
            l = " ".join(parts)
        if low.startswith("set framerate"):
            l = f"Set Framerate {framerate}"
        out.append(l)
        if not inserted and not have_fr and (low.startswith("output") or low.startswith("set ") or low.startswith("require")):
            pass
    if not have_fr:
        idx = 0
        for n, l in enumerate(out):
            low = l.lower()
            if low.startswith("output") or low.startswith("set ") or low.startswith("require"):
                idx = n + 1
        out.insert(idx, f"Set Framerate {framerate}")
    return "\n".join(out) + "\n"


def retarget_output(tape, ext):
    def repl(m):
        base = m.group(1).rsplit(".", 1)[0]
        return f"Output {base}.{ext}"
    return re.sub(r"^Output\s+(\S+)", repl, tape, flags=re.MULTILINE)


def main():
    ap = argparse.ArgumentParser(description="Convert annotated markdown into a VHS .tape file")
    ap.add_argument("input", help="markdown file (e.g. Documentation/sessions.md)")
    ap.add_argument("-o", "--output", help="tape file (default: stdout)")
    ap.add_argument("--fast", action="store_true", help="dev preview: low framerate, half size, 3x shorter sleeps, GIF output")
    ap.add_argument("--speed", type=float, default=1.0, help="divide all Sleep durations by this factor")
    ap.add_argument("--framerate", type=int, default=None, help="force Set Framerate (fewer frames = faster encode); independent of sleeps")
    ap.add_argument("--format", help="override output extension (e.g. gif, webm, mp4)")
    ap.add_argument("--script", action="store_true", help="emit a runnable bash script of the typed commands (for live debugging) instead of a tape")
    ap.add_argument("--human", action="store_true", help="vary typing speed, add word pauses and occasional typo+backspace (deterministic/seeded)")
    args = ap.parse_args()

    with open(args.input) as f:
        src = f.read()

    if args.script:
        tape = extract_script(src)
        if args.output:
            d = os.path.dirname(args.output)
            if d:
                os.makedirs(d, exist_ok=True)
            with open(args.output, "w") as f:
                f.write(tape)
            os.chmod(args.output, 0o755)
            print(f"wrote {args.output}", file=sys.stderr)
        else:
            sys.stdout.write(tape)
        return

    tape = convert(src, human=args.human)

    speed = args.speed
    if args.fast and speed == 1.0:
        speed = 3.0
    if speed != 1.0:
        tape = scale_sleeps(tape, speed)
    if args.fast:
        tape = apply_fast(tape, args.framerate or 24, 0.5)
    elif args.framerate:
        tape = apply_fast(tape, args.framerate, 1.0)
    fmt = args.format or ("gif" if args.fast else None)
    if fmt:
        tape = retarget_output(tape, fmt)

    if args.output:
        d = os.path.dirname(args.output)
        if d:
            os.makedirs(d, exist_ok=True)
        with open(args.output, "w") as f:
            f.write(tape)
        print(f"wrote {args.output}", file=sys.stderr)
    else:
        sys.stdout.write(tape)


if __name__ == "__main__":
    main()
