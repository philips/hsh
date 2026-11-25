#!/usr/bin/env bash
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
work=${SPIKE_DIR:-$HOME/.cache/gvx-spike}
image=${IMAGE:-ghcr.io/philips/hsh-claude-code:latest}
mtu=${MTU:-65520}
export TMPDIR=$work/tmp

"$here/run.sh" -pull missing -image "$image" -- true >/dev/null 2>&1 || { echo "build or pull failed; run ./run.sh -image $image -- true" >&2; exit 1; }
spike=$work/spike
d=$(mktemp -d "$work/link.XXXX")
chmod 700 "$d"
pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null; done
  wait 2>/dev/null
  rm -rf "$d"
}
trap cleanup EXIT

wait_for() {
  for _ in $(seq 100); do [ -S "$1" ] && return 0; sleep 0.1; done
  return 1
}
booted() {
  for _ in $(seq 300); do grep -q 'new took' "$1" 2>/dev/null && return 0; sleep 0.1; done
  return 1
}
x() { "$spike" exec -control "$d/control.sock" -- sh -c "$1" 2>&1 | grep -v -e '^spike: capabilities' -e '^spike: guest pid'; }

start_peer() {
  "$spike" peer -listen "$d/link.sock" -mtu "$mtu" > "$d/peer.log" 2>&1 &
  peer=$!
  pids+=("$peer")
  wait_for "$d/link.sock"
}

start_peer
"$spike" -pull never -link "$d/link.sock" -link-mtu "$mtu" -control "$d/control.sock" \
  -root-volume "$d/root.ext4" -checkpoint-dir "$d/bundle" -image "$image" -- \
  sh -c 'i=0; while :; do i=$((i+1)); echo $i > /tmp/counter; sleep 0.2; done' > "$d/sb1.log" 2>&1 &
pids+=($!)
mkdir -p "$d/bundle"
booted "$d/sb1.log" || { echo "sandbox did not start:"; cat "$d/sb1.log"; exit 1; }
sleep 1

echo "== lockdown"
grep -o 'platform=[a-z]*' "$d/sb1.log"
grep -o 'NetworkOutboundBlocked:[a-z]*' "$d/sb1.log"
echo "== DNS and HTTPS through the peer"
x 'getent hosts example.com | head -1; curl -sS -o /dev/null -w "https %{http_code} in %{time_total}s\n" --max-time 10 https://example.com/'
echo "== throughput (MTU $mtu)"
x 'exec 3</dev/tcp/10.9.9.9/81; s=$(date +%s%N); n=$(cat <&3 | wc -c); e=$(( ($(date +%s%N)-s)/1000000 )); echo "download $((n>>20)) MiB in ${e} ms: $(( n/1000/(e+1) )) MB/s"'
x 's=$(date +%s%N); head -c 268435456 /dev/zero > /dev/tcp/10.9.9.9/82; e=$(( ($(date +%s%N)-s)/1000000 )); echo "upload 256 MiB in ${e} ms: $(( 268435456/1000/(e+1) )) MB/s"'
echo "== checkpoint, then restore onto a new connection"
before=$(x 'cat /tmp/counter')
"$spike" call -control "$d/control.sock" lifecycle.checkpoint '{"leave_running":false}' >/dev/null 2>&1
sleep 1
rm -f "$d/control.sock"
"$spike" -pull never -link "$d/link.sock" -link-mtu "$mtu" -control "$d/control.sock" \
  -root-volume "$d/root.ext4" -restore "$d/bundle" -image "$image" > "$d/sb2.log" 2>&1 &
pids+=($!)
booted "$d/sb2.log" || { echo "restore did not start:"; cat "$d/sb2.log"; exit 1; }
sleep 1
after=$(x 'cat /tmp/counter')
echo "counter before checkpoint $before, after restore $after (larger means the guest kept its state and runs)"
x 'curl -sS -o /dev/null -w "https after restore %{http_code}\n" --max-time 10 https://example.com/'
echo "== peer dies"
kill "$peer"
sleep 1
x 'curl -sS -o /dev/null --max-time 5 https://example.com/; echo "curl exit $? (nonzero: no network without the peer)"'
x 'echo "sandbox still answers"'
echo "== peer log"
cat "$d/peer.log"
