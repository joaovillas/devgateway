#!/usr/bin/env bash
# Brings up the example environment: both toy services and the gateway in
# front of them. Ctrl+C shuts everything down.
#
#   examples/run.sh            bring everything up reusing examples/.run/,
#                              with whatever learning and the panel wrote
#                              before; the copy is only created if missing
#   CLEAN=1 examples/run.sh    discard examples/.run/ and start over from the
#                              pristine example (drops the services and rules
#                              stored there)
#   WEB=1 examples/run.sh      build the panel (web/dist) before the gateway;
#                              without it, the binary serves the placeholder
#
# Ports: gateway 8080 (traffic) and 8081 (admin and panel), payments 9001,
# catalog 9002. The toys accept PAYMENTS_ADDR, CATALOG_ADDR and CATALOG_FAIL;
# changing the addresses means adjusting the routes' upstream.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
work="$here/.run"
exe="$(cd "$root" && go env GOEXE)"

# By default the working copy is kept: it holds the services and rules the
# user created from the panel, plus whatever learning wrote.
if [[ "${CLEAN:-}" == 1 || ! -f "$work/gateway.json" ]]; then
	if [[ -d "$work" ]]; then
		backup="$work.bak-$(date +%Y%m%d-%H%M%S)"
		echo "keeping the previous configuration in $backup"
		mv "$work" "$backup"
	fi
	mkdir -p "$work"
	cp "$here/gateway.json" "$work/"
	cp -r "$here/routes" "$work/routes"
fi
mkdir -p "$work/bin"

if [[ "${WEB:-}" == 1 ]]; then
	echo "building the panel..."
	(cd "$root/web" && npm ci && npm run build)
fi

echo "building..."
(cd "$root" &&
	go build -o "$work/bin/devgateway$exe" ./cmd/devgateway &&
	go build -o "$work/bin/payments$exe" ./examples/toys/payments &&
	go build -o "$work/bin/catalog$exe" ./examples/toys/catalog)

payments_addr="${PAYMENTS_ADDR:-127.0.0.1:9001}"
catalog_addr="${CATALOG_ADDR:-127.0.0.1:9002}"

# In Git Bash (MSYS), the shell's kill does not reach the native Windows
# process: keep the Windows PID too, so taskkill can stop it.
pids=()
winpids=()
start() {
	"$@" &
	pids+=($!)
	if [[ -r /proc/$!/winpid ]]; then
		winpids+=("$(cat /proc/$!/winpid)")
	fi
}
cleanup() {
	trap - EXIT INT TERM
	for pid in "${pids[@]}"; do
		kill "$pid" 2>/dev/null || true
	done
	for wp in "${winpids[@]}"; do
		taskkill //F //T //PID "$wp" >/dev/null 2>&1 || true
	done
	wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Wait for a toy's /health to answer, so the gateway does not start before
# its upstreams and the first request does not report the upstream as down.
ready() {
	command -v curl >/dev/null || { sleep 1; return; }
	for _ in $(seq 50); do
		curl -fs -o /dev/null "http://$1/health" && return
		sleep 0.2
	done
	echo "warning: $1 did not answer /health" >&2
}

start "$work/bin/payments$exe" -addr "$payments_addr"
start "$work/bin/catalog$exe" -addr "$catalog_addr" -fail "${CATALOG_FAIL:-0.25}"
ready "$payments_addr"
ready "$catalog_addr"
start "$work/bin/devgateway$exe" -config "$work/gateway.json"

cat <<'EOF'

example environment is up (Ctrl+C stops it):
  panel and API  http://localhost:8081
  traffic        http://localhost:8080

  passthrough           curl -s localhost:8080/catalog/products
  flaky upstream        curl -si localhost:8080/catalog/stock/p1
  forced override       curl -si -X POST localhost:8080/payments/charges \
                          -H 'X-Scenario: declined' -d '{"amount":4990}'
  without the override  curl -si -X POST localhost:8080/payments/charges -d '{"amount":4990}'
  30% of the calls      for i in $(seq 10); do curl -s -o /dev/null -w '%{http_code} ' localhost:8080/payments/balance; done
  latency 200-900 ms    curl -s -w '\n%{time_total}s\n' localhost:8080/payments/charges/ch_1
  SSE                   curl -sN localhost:8080/payments/events
  learning              curl -s localhost:8080/catalog/search?q=mug; cat examples/.run/routes/catalog.yaml
  port swap             curl -s -X PATCH localhost:8081/api/settings \
                          -H 'Content-Type: application/json' -d '{"ports":{"traffic":9080}}'

EOF

# Shut down when any of the processes exits (or on Ctrl+C).
wait -n "${pids[@]}"
