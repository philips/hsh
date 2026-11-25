#!/usr/bin/env bash
set -euo pipefail

image=${IMAGE:-ghcr.io/philips/hsh-claude-code:latest}
work=${SPIKE_DIR:-$HOME/.cache/gvx-spike}
spike=$work/spike
runsc=${HSH_RUNSC:-}
if [ -z "$runsc" ]; then
  for f in "${XDG_STATE_HOME:-$HOME/.local/state}"/hsh/gvisor/*/runsc; do
    [ -x "$f" ] && runsc=$f && break
  done
fi
export TMPDIR=$work/tmp

read -r -d '' script <<'EOF' || true
t() { local s=$(date +%s%N); "$@" >/dev/null 2>&1; echo "$(( ($(date +%s%N) - s) / 1000000 ))"; }
r() { printf '%-28s %6s ms\n' "$1" "$(t sh -c "$2")"; }
r "claude --version x5" 'for i in 1 2 3 4 5; do claude --version; done'
r "python3 -c pass x20" 'for i in $(seq 20); do python3 -c pass; done'
r "sh -c true x200" 'for i in $(seq 200); do sh -c true; done'
r "copy /usr/lib64 to rootfs" 'cp -r /usr/lib64 /tmp/l && rm -rf /tmp/l'
r "2000 files in /workspace" 'mkdir /workspace/b && cd /workspace/b && for i in $(seq 2000); do echo $i > f$i; done'
r "git add+commit 2000 files" 'cd /workspace/b && git init -q && git add . && git -c user.email=a@b -c user.name=t commit -qm x'
r "git status x10" 'cd /workspace/b && for i in $(seq 10); do git status --short; done'
r "find /usr | wc -l" 'find /usr | wc -l'
r "rm -rf 2000 files" 'rm -rf /workspace/b'
EOF

run_gvx() {
  local ws; ws=$(mktemp -d "$work/ws.XXXX")
  local st=0
  "$spike" -pull never -workspace "$ws" -image "$image" -- sh -c "$script" 2>"$work/bench-gvx.err" || st=$?
  rm -rf "$ws"
  return $st
}

run_runsc() {
  local ws wrap; ws=$(mktemp -d "$work/ws.XXXX")
  wrap=$work/runsc-wrap
  printf '#!/bin/sh\nexec %s --ignore-cgroups --overlay2=none "$@"\n' "$runsc" > "$wrap"
  chmod +x "$wrap"
  local st=0
  podman run --rm --runtime "$wrap" --security-opt label=disable --network none \
    -v "$ws:/workspace" "$image" sh -c "$script" 2>"$work/bench-runsc.err" || st=$?
  rm -rf "$ws"
  return $st
}

run_crun() {
  local ws; ws=$(mktemp -d "$work/ws.XXXX")
  local st=0
  podman run --rm --security-opt label=disable --network none \
    -v "$ws:/workspace" "$image" sh -c "$script" 2>"$work/bench-crun.err" || st=$?
  rm -rf "$ws"
  return $st
}

now_ms() { perl -MTime::HiRes=time -e 'printf "%d\n", time * 1000'; }

runtimes=${RUNTIMES:-gvx runsc crun}
for rt in $runtimes; do
  if [ "$rt" != gvx ] && ! command -v podman >/dev/null; then
    echo "== $rt: skipped (no podman)"
    continue
  fi
  if [ "$rt" = runsc ] && [ -z "$runsc" ]; then
    echo "== runsc: skipped (no runsc; set HSH_RUNSC)"
    continue
  fi
  echo "== $rt"
  s=$(now_ms)
  "run_$rt" || echo "$rt failed with status $?; see $work/bench-$rt.err"
  echo "total wall incl. start/teardown: $(( $(now_ms) - s )) ms"
done
