#!/usr/bin/env python3
import argparse
import os
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
    return s.replace("\\", "\\\\").replace('"', '\\"')


def convert(text):
    lines = text.splitlines()
    out = []
    i = 0
    n = len(lines)
    have_region = any(REGION.search(l) for l in lines)
    active = not have_region
    pending_run = None

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

            if cmd == "output":
                emit(f'Output {m.group("args").strip()}')
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
            elif cmd == "hide":
                emit("Hide")
            elif cmd == "show":
                emit("Show")
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

            speed = kv.get("typingspeed")
            if speed:
                emit(f"Set TypingSpeed {speed}")
            for cmdline in block:
                if not cmdline.strip():
                    continue
                emit(f'Type "{esc(cmdline)}"')
                if "noenter" not in flags:
                    emit("Enter")
            if speed:
                emit("Set TypingSpeed 60ms")
            sleep = kv.get("sleep", "2s")
            if sleep.lower() != "none":
                emit(f"Sleep {sleep}")
            continue

        i += 1

    return "\n".join(out) + "\n"


def main():
    ap = argparse.ArgumentParser(description="Convert annotated markdown into a VHS .tape file")
    ap.add_argument("input", help="markdown file (e.g. Documentation/sessions.md)")
    ap.add_argument("-o", "--output", help="tape file (default: stdout)")
    args = ap.parse_args()

    with open(args.input) as f:
        tape = convert(f.read())

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
