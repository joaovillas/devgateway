#!/usr/bin/env bash
# End-to-end check of the example environment, using nothing but curl and the
# admin API. It starts both toy services and the gateway against a temporary
# copy of examples/ (gateway.json and routes/), exercises every feature and
# shuts everything down at the end. Exits 0 only if every check passed.
#
#   examples/e2e.sh
#
# It checks, in this order:
#   1. probabilistic override: a plausible share of 503s with seed 42, and the
#      same sequence after restarting the process (determinism)
#   2. passthrough: response identical to the upstream one, no intervention
#   3. forced override: a synthesized 402 whenever the criteria match
#   4. latency range: a 200 ms to 900 ms delay, and the captured exchange
#      waterfall with the injected time separate from the upstream time
#   5. learning mode: a new path becomes a disabled override in the route YAML
#   6. hot port swap: traffic and admin move to other ports without restarting
#      the process
#
# Needs bash, curl and Go. Uses ports 8080, 8081, 9001, 9002, 9080 and 9081,
# which have to be free.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
exe="$(cd "$root" && go env GOEXE)"
work="$(mktemp -d "${TMPDIR:-/tmp}/devgateway-e2e.XXXXXX")"

T=http://127.0.0.1:8080
A=http://127.0.0.1:8081

passed=0
failed=0
ok() { passed=$((passed + 1)); printf '  ok     %s\n' "$1"; }
bad() { failed=$((failed + 1)); printf '  FAILED %s\n' "$1"; }
check() { # check "description" command...
	local desc=$1
	shift
	if "$@"; then ok "$desc"; else bad "$desc"; fi
}
section() { printf '\n%s\n' "$1"; }

# --- processes ---------------------------------------------------------------

# In Git Bash (MSYS), the shell kill does not reach the native Windows
# process: keep the Windows PID too, so taskkill can stop it.
declare -A pid winpid
start() { # start name command...
	local name=$1
	shift
	"$@" >"$work/$name.log" 2>&1 &
	pid[$name]=$!
	winpid[$name]=
	if [[ -r /proc/$!/winpid ]]; then
		winpid[$name]="$(cat /proc/$!/winpid)"
	fi
}
stop() { # stop name
	local name=$1
	[[ -n "${pid[$name]:-}" ]] || return 0
	kill "${pid[$name]}" 2>/dev/null || true
	if [[ -n "${winpid[$name]}" ]]; then
		taskkill //F //T //PID "${winpid[$name]}" >/dev/null 2>&1 || true
	fi
	wait "${pid[$name]}" 2>/dev/null || true
	unset "pid[$name]"
}
cleanup() {
	trap - EXIT INT TERM
	for name in "${!pid[@]}"; do stop "$name"; done
	if [[ $failed -eq 0 && -z "${KEEP_WORK:-}" ]]; then
		rm -rf "$work"
	else
		echo "files and logs from this run: $work"
	fi
}
trap cleanup EXIT INT TERM

wait_http() { # wait_http url: up to 10 s for any HTTP response
	for _ in $(seq 50); do
		curl -s -o /dev/null "$1" && return 0
		sleep 0.2
	done
	echo "no response from $1" >&2
	return 1
}

port_free() { ! curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$1/"; }
for p in 8080 8081 9001 9002 9080 9081; do
	port_free "$p" || { echo "port $p is already in use; free it before running" >&2; exit 2; }
done

# A clean copy of gateway.json and routes/: the gateway rewrites the documents
# (learning and the port swap both write to them) and the comments are lost.
fresh_config() {
	rm -rf "$work/cfg"
	mkdir -p "$work/cfg"
	cp "$here/gateway.json" "$work/cfg/"
	cp -r "$here/routes" "$work/cfg/routes"
}
start_gateway() {
	start devgateway "$work/bin/devgateway$exe" -config "$work/cfg/gateway.json"
	wait_http "$A/api/status"
}

echo "building into $work/bin..."
mkdir -p "$work/bin"
(cd "$root" &&
	go build -o "$work/bin/devgateway$exe" ./cmd/devgateway &&
	go build -o "$work/bin/payments$exe" ./examples/toys/payments &&
	go build -o "$work/bin/catalog$exe" ./examples/toys/catalog)

start payments "$work/bin/payments$exe" -addr 127.0.0.1:9001
start catalog "$work/bin/catalog$exe" -addr 127.0.0.1:9002 -fail 0.25
wait_http http://127.0.0.1:9001/health
wait_http http://127.0.0.1:9002/health
fresh_config
start_gateway

# --- HTTP and JSON helpers ---------------------------------------------------

# req method url [curl args...]: stores the status, headers and body in
# $code, $work/h and $work/b.
req() {
	local method=$1 url=$2
	shift 2
	code=$(curl -s -X "$method" -D "$work/h" -o "$work/b" -w '%{http_code}' "$@" "$url")
}
header() { # header name: that header value in the last response
	tr -d '\r' <"$work/h" | awk -v n="$(echo "$1" | tr 'A-Z' 'a-z')" '
		{ i = index($0, ":"); if (i && tolower(substr($0, 1, i - 1)) == n) { print substr($0, i + 2); exit } }'
}
# fields field < json: the numeric or textual values of "field" in indented
# JSON (the API answers one key per line), in the order they appear.
fields() {
	sed -n "s/^ *\"$1\": *\"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}$/\1/p"
}
api() { curl -s "$A$1"; }

# --- 1. probabilistic override -----------------------------------------------

section "1. probabilistic override (balance-flaky, probability 0.3, seed 42)"

# First series right after startup: the roll for each request derives from
# (seed, sequence number), so the series repeats after a restart.
series() {
	local s=""
	for _ in $(seq 200); do
		s+="$(curl -s -o /dev/null -w '%{http_code}' "$T/payments/balance") "
	done
	echo "$s"
}
run1=$(series)
n503=$(grep -o 503 <<<"$run1" | wc -l | tr -d ' ')
n200=$(grep -o 200 <<<"$run1" | wc -l | tr -d ' ')
echo "         200 requests: $n503 x synthesized 503, $n200 x 200 from the upstream"
# Binomial(200, 0.3): mean 60, stddev 6.5. Accepts 36 to 84 (3.7 stddevs).
check "plausible share of 503s ($n503/200, expected ~60)" \
	test "$n503" -ge 36 -a "$n503" -le 84 -a $((n503 + n200)) -eq 200
synth=$(api "/api/exchanges?override=payments/balance-flaky&limit=500" | fields outcome | grep -c synthesized || true)
check "the history credits all $n503 503s to the payments/balance-flaky override" test "$synth" -eq "$n503"
req GET "$T/payments/balance"
while [[ $code != 503 ]]; do req GET "$T/payments/balance"; done
check "the synthesized 503 carries X-Gateway with the override and intervention" \
	test "$(header X-Gateway)" = "route=payments; override=payments/balance-flaky; intervention=synthesized"

stop devgateway
fresh_config
start_gateway
run2=$(series)
check "same seed, same arrival order: the series repeats after a restart" test "$run1" = "$run2"

# --- 2. passthrough ----------------------------------------------------------

section "2. passthrough (catalog route, no override declared)"

direct=$(curl -s http://127.0.0.1:9002/products)
req GET "$T/catalog/products"
check "GET /catalog/products answers 200" test "$code" = 200
check "body identical to the upstream one" test "$(cat "$work/b")" = "$direct"
check "X-Gateway names the route only" test "$(header X-Gateway)" = "route=catalog"
req POST "$T/payments/charges" -H 'Content-Type: application/json' -d '{"amount":4990}'
check "POST /payments/charges without X-Scenario goes to the upstream (201)" test "$code" = 201
check "... and X-Gateway reports no intervention" test "$(header X-Gateway)" = "route=payments"

# --- 3. forced override ------------------------------------------------------

section "3. forced override (charge-declined, only with X-Scenario: declined)"

all402=true
for _ in $(seq 20); do
	req POST "$T/payments/charges" -H 'X-Scenario: declined' -d '{"amount":4990}'
	[[ $code == 402 ]] || all402=false
done
check "20 out of 20 requests carrying the header get a 402" $all402
check "the body declared in the YAML (card_declined)" grep -q '"error": *"card_declined"' "$work/b"
check "X-Gateway with the override and the intervention" \
	test "$(header X-Gateway)" = "route=payments; override=payments/charge-declined; intervention=synthesized"
id=$(api "/api/exchanges?override=payments/charge-declined&limit=1" | fields id | head -1)
ex=$(api "/api/exchanges/$id")
check "exchange captured with outcome synthesized" grep -q '"outcome": "synthesized"' <<<"$ex"
check "... and no upstream time (upstreamMs 0)" test "$(fields upstreamMs <<<"$ex")" = 0

# --- 4. latency range and waterfall ------------------------------------------

section "4. latency range (charge-lookup-slow, 200 ms to 900 ms)"

times=()
for _ in $(seq 15); do
	times+=("$(curl -s -o /dev/null -w '%{time_total}' "$T/payments/charges/ch_1")")
done
req GET "$T/payments/charges/ch_1"
check "the response comes from the upstream (200)" test "$code" = 200
check "X-Gateway marks the delayed intervention" \
	test "$(header X-Gateway)" = "route=payments; override=payments/charge-lookup-slow; intervention=delayed"
check "total time at the client >= 200 ms in all 15" \
	awk 'BEGIN { for (i = 1; i < ARGC; i++) if (ARGV[i] < 0.2) exit 1 }' "${times[@]}"

list=$(api "/api/exchanges?override=payments/charge-lookup-slow&limit=16")
paste <(fields totalMs <<<"$list") <(fields upstreamMs <<<"$list") \
	<(fields injectedMs <<<"$list") <(fields gatewayMs <<<"$list") >"$work/waterfall"
echo "         totalMs  upstreamMs  injectedMs  gatewayMs (3 most recent)"
head -3 "$work/waterfall" | awk '{ printf "         %7.1f  %10.1f  %10.1f  %9.1f\n", $1, $2, $3, $4 }'
check "16 exchanges captured" test "$(wc -l <"$work/waterfall" | tr -d ' ')" = 16
check "injected time within [200, 900] ms in all of them" \
	awk '$3 < 200 || $3 > 900 { exit 1 }' "$work/waterfall"
check "injected time rolled across the range (not constant: spread > 100 ms)" \
	awk 'NR == 1 { lo = hi = $3 } { if ($3 < lo) lo = $3; if ($3 > hi) hi = $3 } END { exit !(hi - lo > 100) }' "$work/waterfall"
check "total = upstream + injected + gateway (1 ms tolerance)" \
	awk '{ d = $1 - ($2 + $3 + $4); if (d < -1 || d > 1) exit 1 }' "$work/waterfall"
ex=$(api "/api/exchanges/$(fields id <<<"$list" | head -1)")
check "exchange with outcome upstream and a delayed intervention" \
	grep -q '"outcome": "upstream"' <<<"$ex"
check "... listed under interventions" grep -q '"delayed"' <<<"$ex"

# The toy GET /charges/{id} answers in under a millisecond, below the clock
# resolution on some systems. To see the waterfall with both quantities
# actually measured, an override created through the API delays the catalog
# search, whose upstream already takes 150 to 450 ms on its own.
section "4b. waterfall over a slow upstream (search-slow override created via the API)"

req POST "$A/api/routes/catalog/overrides" -H 'Content-Type: application/json' \
	-d '{"name":"search-slow","match":{"path":"/catalog/search","method":"GET"},"latency":{"min":"500ms","max":"800ms"}}'
check "latency-only override created (201)" test "$code" = 201
for _ in $(seq 8); do curl -s -o /dev/null "$T/catalog/search?q=mug"; done
list=$(api "/api/exchanges?override=catalog/search-slow&limit=8")
paste <(fields totalMs <<<"$list") <(fields upstreamMs <<<"$list") \
	<(fields injectedMs <<<"$list") <(fields gatewayMs <<<"$list") >"$work/waterfall"
echo "         totalMs  upstreamMs  injectedMs  gatewayMs (3 most recent)"
head -3 "$work/waterfall" | awk '{ printf "         %7.1f  %10.1f  %10.1f  %9.1f\n", $1, $2, $3, $4 }'
check "8 exchanges captured" test "$(wc -l <"$work/waterfall" | tr -d ' ')" = 8
check "upstream measured within the range of the service itself (150 to 500 ms)" \
	awk '$2 < 150 || $2 > 500 { exit 1 }' "$work/waterfall"
check "injected time within the declared range (500 to 800 ms), upstream not added in" \
	awk '$3 < 500 || $3 > 800 { exit 1 }' "$work/waterfall"
check "total = upstream + injected + gateway (1 ms tolerance)" \
	awk '{ d = $1 - ($2 + $3 + $4); if (d < -1 || d > 1) exit 1 }' "$work/waterfall"
req DELETE "$A/api/routes/catalog/overrides/search-slow"
check "override deleted through the API" test "${code:0:1}" = 2

# --- 5. learning mode --------------------------------------------------------

section "5. learning mode (learning.enabled in gateway.json)"

yaml="$work/cfg/routes/catalog.yaml"
check "catalog.yaml starts with no override for /catalog/products/p3" \
	bash -c "! grep -q 'path: /catalog/products/p3$' '$yaml'"
req GET "$T/catalog/products/p3"
check "the new path goes through to the upstream (200)" test "$code" = 200
learned=false
for _ in $(seq 25); do
	if grep -q 'path: /catalog/products/p3$' "$yaml"; then learned=true; break; fi
	sleep 0.2
done
check "override written to the route YAML" $learned
block=$(awk '/^  - name: get-catalog-products-p3$/{ on = 1; print; next } on && /^  - name: /{ exit } on' "$yaml")
check "... named get-catalog-products-p3" test -n "$block"
check "... turned off (enabled: false)" grep -q '^    enabled: false$' <<<"$block"
check "... with method GET" grep -q '^      method: GET$' <<<"$block"
check "... with the observed response (status 200 and product p3)" \
	grep -q 'status: 200' <<<"$block"
check "... and the upstream body" grep -q 'Mechanical pencil' <<<"$block"
check "... with source.kind learned and the originating exchange" \
	bash -c "grep -q 'kind: learned' <<<\"\$1\" && grep -q 'exchange: ' <<<\"\$1\"" _ "$block"
check "the API shows the learned override turned off" \
	bash -c "curl -s '$A/api/routes/catalog/overrides/get-catalog-products-p3' | grep -q '\"enabled\": false'"
req GET "$T/catalog/products/p3"
check "turned off, it does not intervene: the next request goes to the upstream" \
	test "$code:$(header X-Gateway)" = "200:route=catalog"
sleep 0.5
check "and an endpoint already known is not learned again" \
	test "$(grep -c 'path: /catalog/products/p3$' "$yaml")" = 1

# --- 6. hot port swap --------------------------------------------------------

section "6. hot port swap (PATCH /api/settings)"

started=$(api /api/status | fields startedAt)
patch() { # patch url json
	curl -s -o "$work/b" -w '%{http_code}' -X PATCH "$1/api/settings" \
		-H 'Content-Type: application/merge-patch+json' -d "$2"
}
check "traffic 8080 -> 9080 accepted (200)" test "$(patch "$A" '{"ports":{"traffic":9080}}')" = 200
check "gateway.json rewritten with the new port" grep -q '"traffic": 9080' "$work/cfg/gateway.json"
check "9080 serves the traffic" test "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:9080/catalog/products)" = 200
check "8080 stopped accepting connections" port_free 8080
check "admin 8081 -> 9081 accepted (200)" test "$(patch "$A" '{"ports":{"admin":9081}}')" = 200
check "the API answers on 9081" \
	bash -c "curl -s http://127.0.0.1:9081/api/status | grep -q '\"admin\": 9081'"
check "8081 stopped accepting connections" port_free 8081
check "same process, no restart (startedAt unchanged)" \
	test "$(curl -s http://127.0.0.1:9081/api/status | fields startedAt)" = "$started"
check "back to ports 8080 and 8081 (200)" \
	test "$(patch http://127.0.0.1:9081 '{"ports":{"traffic":8080,"admin":8081}}')" = 200
check "8080 serves again" test "$(curl -s -o /dev/null -w '%{http_code}' "$T/catalog/products")" = 200
check "8081 serves again" bash -c "curl -s '$A/api/status' | grep -q '\"traffic\": 8080'"
check "9080 and 9081 were closed" bash -c "! curl -s -o /dev/null --max-time 1 http://127.0.0.1:9080/ && ! curl -s -o /dev/null --max-time 1 http://127.0.0.1:9081/"

# --- summary -----------------------------------------------------------------

printf '\n%d checks passed, %d failed\n' "$passed" "$failed"
[[ $failed -eq 0 ]]
