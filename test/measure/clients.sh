#!/usr/bin/env bash
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${1:-/tmp/measure}"
mkdir -p "$OUT"
(cd "$HERE/../.." && go build -o "$OUT/denyproxy" ./test/measure/denyproxy && go build -o "$OUT/gofetch" ./test/measure/gofetch) || exit 1

HOST="blocked.example.invalid"
PHRASE="hsh: blocked by sandbox policy - needs user approval - do not work around - retry later"
NEWBODY="hsh: egress to $HOST is blocked by the sandbox policy and needs the user's approval.
The user can approve it with \`hsh monitor\`. Do not look for workarounds or alternative hosts.
Wait a minute or two and retry the same request, or tell the user which host you need.
"
OLDBODY="hsh: egress to $HOST is not allowed (not in network.allow)
review denied hosts with: hsh proxy-denied
"
SMOKEMSG="Egress proxying is denied to host '$HOST': connection not allowed by ACL."
SMOKEBODY="$SMOKEMSG

Proxy denied
"

declare -A VARIANTS
VARIANTS[old]="--status 407 --body @OLD"
VARIANTS[407]="--status 407 --body @NEW"
VARIANTS[407-reason]="--status 407 --reason @PHRASE --body @NEW"
VARIANTS[403-reason]="--status 403 --reason @PHRASE --body @NEW"
VARIANTS[503-retry]="--status 503 --reason @PHRASE --header Retry-After:60 --body @NEW"
VARIANTS[400-reason]="--status 400 --reason @PHRASE --body @NEW"
VARIANTS[511-reason]="--status 511 --reason @PHRASE --body @NEW"
VARIANTS[smokescreen]="--status 407 --header SMOKE --body @SMOKE"
ORDER=(${MEASURE_VARIANTS:-old 407 407-reason 403-reason 503-retry 400-reason 511-reason smokescreen})

CLIENTS=(curl wget py-urllib py-requests node-fetch npm git go)
TSV="$OUT/results.tsv"
printf 'variant\tclient\tscheme\texit\tseconds\trequests\toutput\n' >"$TSV"

run_client() {
	local client="$1" url="$2"
	case "$client" in
	curl) curl -sS -m 30 "$url" ;;
	wget) wget -O- -T 30 -t 1 "$url" ;;
	py-urllib) python3 -c "import sys,urllib.request as u; print(u.urlopen(sys.argv[1], timeout=30).read())" "$url" ;;
	py-requests) python3 -c "import sys,requests; r=requests.get(sys.argv[1], timeout=30); print(r.status_code, r.reason)" "$url" ;;
	node-fetch) NODE_USE_ENV_PROXY=1 node -e "fetch(process.argv[1]).then(async r=>console.log('status', r.status, r.statusText)).catch(e=>console.log(String(e), '| cause:', e.cause && String(e.cause)))" "$url" ;;
	npm) npm view left-pad version --registry "${url%/data.json}/" --userconfig /dev/null --cache "$OUT/npmcache" --fetch-retries=2 --fetch-retry-mintimeout=1000 --fetch-retry-maxtimeout=3000 ;;
	git) GIT_TERMINAL_PROMPT=0 git ls-remote "${url%/data.json}/x/y.git" ;;
	go) "$OUT/gofetch" "$url" ;;
	esac
}

for v in "${ORDER[@]}"; do
	args="${VARIANTS[$v]}"
	args="${args//@OLD/$OLDBODY}"
	argv=()
	read -r -a toks <<<"${VARIANTS[$v]}"
	argv=()
	i=0
	while [ $i -lt ${#toks[@]} ]; do
		t="${toks[$i]}"
		case "$t" in
		--status) argv+=(--status "${toks[$((i + 1))]}"); i=$((i + 2)) ;;
		--reason) argv+=(--reason "$PHRASE"); i=$((i + 2)) ;;
		--header)
			case "${toks[$((i + 1))]}" in
			SMOKE) argv+=(--header "X-Smokescreen-Error: $SMOKEMSG") ;;
			*) argv+=(--header "Retry-After: 60") ;;
			esac
			i=$((i + 2))
			;;
		--body)
			case "${toks[$((i + 1))]}" in
			@OLD) argv+=(--body "$OLDBODY") ;;
			@NEW) argv+=(--body "$NEWBODY") ;;
			@SMOKE) argv+=(--body "$SMOKEBODY") ;;
			esac
			i=$((i + 2))
			;;
		*) i=$((i + 1)) ;;
		esac
	done
	rm -f "$OUT/port" "$OUT/proxy.log"
	"$OUT/denyproxy" --port-file "$OUT/port" "${argv[@]}" >"$OUT/proxy.log" 2>&1 &
	PPID_DENY=$!
	for _ in $(seq 1 50); do [ -s "$OUT/port" ] && break; sleep 0.1; done
	ADDR="$(cat "$OUT/port")"
	export http_proxy="http://$ADDR" https_proxy="http://$ADDR" HTTP_PROXY="http://$ADDR" HTTPS_PROXY="http://$ADDR" no_proxy="" NO_PROXY=""
	for c in "${CLIENTS[@]}"; do
		for scheme in https http; do
			url="$scheme://$HOST/data.json"
			: >"$OUT/proxy.log"
			start=$(date +%s.%N)
			output="$(timeout 90 bash -c "$(declare -f run_client); OUT='$OUT'; run_client '$c' '$url'" 2>&1)"
			code=$?
			end=$(date +%s.%N)
			reqs="$(grep -c '^request ' "$OUT/proxy.log" || true)"
			secs="$(awk -v a="$start" -v b="$end" 'BEGIN { printf "%.1f", b - a }')"
			flat="$(printf '%s' "$output" | tr '\n\t' '  ' | cut -c1-400)"
			printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$v" "$c" "$scheme" "$code" "$secs" "$reqs" "$flat" >>"$TSV"
			echo "[$v] $c $scheme exit=$code ${secs}s requests=$reqs"
		done
	done
	kill "$PPID_DENY" 2>/dev/null
	wait "$PPID_DENY" 2>/dev/null
done
echo "results: $TSV"
