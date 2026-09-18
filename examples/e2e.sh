#!/usr/bin/env bash
# Verificação de ponta a ponta do ambiente de exemplo, só com curl e a API de
# administração. Sobe os dois serviços de brinquedo e o gateway numa cópia
# temporária de examples/ (gateway.json e routes/), exercita cada recurso e
# encerra tudo no fim. Sai com status 0 só se todas as verificações passarem.
#
#   examples/e2e.sh
#
# Verifica, nesta ordem:
#   1. override probabilístico: proporção plausível de 503 com seed 42 e a
#      mesma sequência depois de reiniciar o processo (determinismo)
#   2. passthrough: resposta idêntica à do upstream, sem intervenção
#   3. override forçado: 402 sintetizado sempre que o critério casa
#   4. latência em intervalo: atraso entre 200 ms e 900 ms, e o waterfall da
#      troca capturada com o tempo injetado separado do tempo do upstream
#   5. modo aprendizado: path novo vira override desligado no YAML da rota
#   6. troca de porta a quente: tráfego e administração mudam de porta sem
#      reiniciar o processo
#
# Precisa de bash, curl e Go. Usa as portas 8080, 8081, 9001, 9002, 9080 e
# 9081, que precisam estar livres.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
exe="$(cd "$root" && go env GOEXE)"
work="$(mktemp -d "${TMPDIR:-/tmp}/gateway-e2e.XXXXXX")"

T=http://127.0.0.1:8080
A=http://127.0.0.1:8081

passed=0
failed=0
ok() { passed=$((passed + 1)); printf '  ok     %s\n' "$1"; }
bad() { failed=$((failed + 1)); printf '  FALHOU %s\n' "$1"; }
check() { # check "descrição" comando...
	local desc=$1
	shift
	if "$@"; then ok "$desc"; else bad "$desc"; fi
}
section() { printf '\n%s\n' "$1"; }

# --- processos ---------------------------------------------------------------

# No Git Bash (MSYS), o kill do shell não chega ao processo nativo do Windows:
# guarda também o PID do Windows para encerrar com taskkill.
declare -A pid winpid
start() { # start nome comando...
	local name=$1
	shift
	"$@" >"$work/$name.log" 2>&1 &
	pid[$name]=$!
	winpid[$name]=
	if [[ -r /proc/$!/winpid ]]; then
		winpid[$name]="$(cat /proc/$!/winpid)"
	fi
}
stop() { # stop nome
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
		echo "arquivos e logs da execução: $work"
	fi
}
trap cleanup EXIT INT TERM

wait_http() { # wait_http url: até 10 s por qualquer resposta HTTP
	for _ in $(seq 50); do
		curl -s -o /dev/null "$1" && return 0
		sleep 0.2
	done
	echo "sem resposta em $1" >&2
	return 1
}

port_free() { ! curl -s -o /dev/null --max-time 1 "http://127.0.0.1:$1/"; }
for p in 8080 8081 9001 9002 9080 9081; do
	port_free "$p" || { echo "a porta $p já está em uso; libere-a antes de rodar" >&2; exit 2; }
done

# Cópia limpa de gateway.json e routes/: o gateway reescreve os documentos (o
# aprendizado e a troca de porta gravam neles) e os comentários se perdem.
fresh_config() {
	rm -rf "$work/cfg"
	mkdir -p "$work/cfg"
	cp "$here/gateway.json" "$work/cfg/"
	cp -r "$here/routes" "$work/cfg/routes"
}
start_gateway() {
	start gateway "$work/bin/gateway$exe" -config "$work/cfg/gateway.json"
	wait_http "$A/api/status"
}

echo "compilando em $work/bin..."
mkdir -p "$work/bin"
(cd "$root" &&
	go build -o "$work/bin/gateway$exe" ./cmd/gateway &&
	go build -o "$work/bin/payments$exe" ./examples/toys/payments &&
	go build -o "$work/bin/catalog$exe" ./examples/toys/catalog)

start payments "$work/bin/payments$exe" -addr 127.0.0.1:9001
start catalog "$work/bin/catalog$exe" -addr 127.0.0.1:9002 -fail 0.25
wait_http http://127.0.0.1:9001/health
wait_http http://127.0.0.1:9002/health
fresh_config
start_gateway

# --- utilitários de HTTP e JSON ----------------------------------------------

# req método url [args do curl...]: grava status, cabeçalhos e corpo em
# $code, $work/h e $work/b.
req() {
	local method=$1 url=$2
	shift 2
	code=$(curl -s -X "$method" -D "$work/h" -o "$work/b" -w '%{http_code}' "$@" "$url")
}
header() { # header nome: valor do cabeçalho na última resposta
	tr -d '\r' <"$work/h" | awk -v n="$(echo "$1" | tr 'A-Z' 'a-z')" '
		{ i = index($0, ":"); if (i && tolower(substr($0, 1, i - 1)) == n) { print substr($0, i + 2); exit } }'
}
# fields campo < json: valores numéricos ou textuais de "campo" em JSON
# indentado (a API responde uma chave por linha), na ordem em que aparecem.
fields() {
	sed -n "s/^ *\"$1\": *\"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}$/\1/p"
}
api() { curl -s "$A$1"; }

# --- 1. override probabilístico ----------------------------------------------

section "1. override probabilístico (balance-flaky, probability 0.3, seed 42)"

# Primeira série logo após a subida: o sorteio de cada requisição deriva de
# (seed, número de sequência), então a série se repete depois de reiniciar.
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
echo "         200 requisições: $n503 × 503 sintetizado, $n200 × 200 do upstream"
# Binomial(200, 0.3): média 60, desvio 6,5. Aceita de 36 a 84 (3,7 desvios).
check "proporção plausível de 503 ($n503/200, esperado ~60)" \
	test "$n503" -ge 36 -a "$n503" -le 84 -a $((n503 + n200)) -eq 200
synth=$(api "/api/exchanges?override=payments/balance-flaky&limit=500" | fields outcome | grep -c synthesized || true)
check "o histórico atribui os $n503 503 ao override payments/balance-flaky" test "$synth" -eq "$n503"
req GET "$T/payments/balance"
while [[ $code != 503 ]]; do req GET "$T/payments/balance"; done
check "503 sintetizado traz X-Gateway com override e intervenção" \
	test "$(header X-Gateway)" = "route=payments; override=payments/balance-flaky; intervention=synthesized"

stop gateway
fresh_config
start_gateway
run2=$(series)
check "mesma seed, mesma ordem de chegada: a série se repete após reiniciar" test "$run1" = "$run2"

# --- 2. passthrough ----------------------------------------------------------

section "2. passthrough (rota catalog, sem override declarado)"

direct=$(curl -s http://127.0.0.1:9002/products)
req GET "$T/catalog/products"
check "GET /catalog/products responde 200" test "$code" = 200
check "corpo idêntico ao do upstream" test "$(cat "$work/b")" = "$direct"
check "X-Gateway identifica só a rota" test "$(header X-Gateway)" = "route=catalog"
req POST "$T/payments/charges" -H 'Content-Type: application/json' -d '{"amount":4990}'
check "POST /payments/charges sem X-Scenario segue ao upstream (201)" test "$code" = 201
check "... e sem intervenção no X-Gateway" test "$(header X-Gateway)" = "route=payments"

# --- 3. override forçado -----------------------------------------------------

section "3. override forçado (charge-declined, só com X-Scenario: declined)"

all402=true
for _ in $(seq 20); do
	req POST "$T/payments/charges" -H 'X-Scenario: declined' -d '{"amount":4990}'
	[[ $code == 402 ]] || all402=false
done
check "20 de 20 requisições com o cabeçalho recebem 402" $all402
check "corpo declarado no YAML (card_declined)" grep -q '"error": *"card_declined"' "$work/b"
check "X-Gateway com override e intervenção" \
	test "$(header X-Gateway)" = "route=payments; override=payments/charge-declined; intervention=synthesized"
id=$(api "/api/exchanges?override=payments/charge-declined&limit=1" | fields id | head -1)
ex=$(api "/api/exchanges/$id")
check "troca capturada com outcome synthesized" grep -q '"outcome": "synthesized"' <<<"$ex"
check "... e sem tempo de upstream (upstreamMs 0)" test "$(fields upstreamMs <<<"$ex")" = 0

# --- 4. latência em intervalo e waterfall ------------------------------------

section "4. latência em intervalo (charge-lookup-slow, 200 ms a 900 ms)"

times=()
for _ in $(seq 15); do
	times+=("$(curl -s -o /dev/null -w '%{time_total}' "$T/payments/charges/ch_1")")
done
req GET "$T/payments/charges/ch_1"
check "resposta vem do upstream (200)" test "$code" = 200
check "X-Gateway marca a intervenção delayed" \
	test "$(header X-Gateway)" = "route=payments; override=payments/charge-lookup-slow; intervention=delayed"
check "tempo total no cliente ≥ 200 ms em todas as 15" \
	awk 'BEGIN { for (i = 1; i < ARGC; i++) if (ARGV[i] < 0.2) exit 1 }' "${times[@]}"

list=$(api "/api/exchanges?override=payments/charge-lookup-slow&limit=16")
paste <(fields totalMs <<<"$list") <(fields upstreamMs <<<"$list") \
	<(fields injectedMs <<<"$list") <(fields gatewayMs <<<"$list") >"$work/waterfall"
echo "         totalMs  upstreamMs  injectedMs  gatewayMs (as 3 mais novas)"
head -3 "$work/waterfall" | awk '{ printf "         %7.1f  %10.1f  %10.1f  %9.1f\n", $1, $2, $3, $4 }'
check "16 trocas capturadas" test "$(wc -l <"$work/waterfall" | tr -d ' ')" = 16
check "injetado dentro de [200, 900] ms em todas" \
	awk '$3 < 200 || $3 > 900 { exit 1 }' "$work/waterfall"
check "injetado sorteado no intervalo (não constante: amplitude > 100 ms)" \
	awk 'NR == 1 { lo = hi = $3 } { if ($3 < lo) lo = $3; if ($3 > hi) hi = $3 } END { exit !(hi - lo > 100) }' "$work/waterfall"
check "total = upstream + injetado + gateway (±1 ms)" \
	awk '{ d = $1 - ($2 + $3 + $4); if (d < -1 || d > 1) exit 1 }' "$work/waterfall"
ex=$(api "/api/exchanges/$(fields id <<<"$list" | head -1)")
check "troca com outcome upstream e intervenção delayed" \
	grep -q '"outcome": "upstream"' <<<"$ex"
check "... listada em interventions" grep -q '"delayed"' <<<"$ex"

# O GET /charges/{id} do brinquedo responde em menos de 1 ms, abaixo da
# resolução do relógio em alguns sistemas. Para ver o waterfall com as duas
# grandezas medidas, um override criado pela API atrasa a busca do catálogo,
# cujo upstream demora de 150 a 450 ms por conta própria.
section "4b. waterfall sobre upstream lento (override search-slow criado pela API)"

req POST "$A/api/routes/catalog/overrides" -H 'Content-Type: application/json' \
	-d '{"name":"search-slow","match":{"path":"/catalog/search","method":"GET"},"latency":{"min":"500ms","max":"800ms"}}'
check "override só de latência criado (201)" test "$code" = 201
for _ in $(seq 8); do curl -s -o /dev/null "$T/catalog/search?q=caneca"; done
list=$(api "/api/exchanges?override=catalog/search-slow&limit=8")
paste <(fields totalMs <<<"$list") <(fields upstreamMs <<<"$list") \
	<(fields injectedMs <<<"$list") <(fields gatewayMs <<<"$list") >"$work/waterfall"
echo "         totalMs  upstreamMs  injectedMs  gatewayMs (as 3 mais novas)"
head -3 "$work/waterfall" | awk '{ printf "         %7.1f  %10.1f  %10.1f  %9.1f\n", $1, $2, $3, $4 }'
check "8 trocas capturadas" test "$(wc -l <"$work/waterfall" | tr -d ' ')" = 8
check "upstream medido no intervalo do próprio serviço (150 a 500 ms)" \
	awk '$2 < 150 || $2 > 500 { exit 1 }' "$work/waterfall"
check "injetado no intervalo declarado (500 a 800 ms), sem somar o upstream" \
	awk '$3 < 500 || $3 > 800 { exit 1 }' "$work/waterfall"
check "total = upstream + injetado + gateway (±1 ms)" \
	awk '{ d = $1 - ($2 + $3 + $4); if (d < -1 || d > 1) exit 1 }' "$work/waterfall"
req DELETE "$A/api/routes/catalog/overrides/search-slow"
check "override removido pela API" test "${code:0:1}" = 2

# --- 5. modo aprendizado -----------------------------------------------------

section "5. modo aprendizado (learning.enabled em gateway.json)"

yaml="$work/cfg/routes/catalog.yaml"
check "catalog.yaml começa sem override para /catalog/products/p3" \
	bash -c "! grep -q 'path: /catalog/products/p3$' '$yaml'"
req GET "$T/catalog/products/p3"
check "path novo passa pelo upstream (200)" test "$code" = 200
learned=false
for _ in $(seq 25); do
	if grep -q 'path: /catalog/products/p3$' "$yaml"; then learned=true; break; fi
	sleep 0.2
done
check "override gravado no YAML da rota" $learned
block=$(awk '/^  - name: get-catalog-products-p3$/{ on = 1; print; next } on && /^  - name: /{ exit } on' "$yaml")
check "... com o nome get-catalog-products-p3" test -n "$block"
check "... desligado (enabled: false)" grep -q '^    enabled: false$' <<<"$block"
check "... com o método GET" grep -q '^      method: GET$' <<<"$block"
check "... com a resposta observada (status 200 e o produto p3)" \
	grep -q 'status: 200' <<<"$block"
check "... e o corpo do upstream" grep -q 'Lapiseira' <<<"$block"
check "... com source.kind learned e a troca de origem" \
	bash -c "grep -q 'kind: learned' <<<\"\$1\" && grep -q 'exchange: ' <<<\"\$1\"" _ "$block"
check "a API mostra o override aprendido desligado" \
	bash -c "curl -s '$A/api/routes/catalog/overrides/get-catalog-products-p3' | grep -q '\"enabled\": false'"
req GET "$T/catalog/products/p3"
check "desligado, ele não intervém: a próxima requisição segue ao upstream" \
	test "$code:$(header X-Gateway)" = "200:route=catalog"
sleep 0.5
check "e o endpoint já conhecido não é aprendido de novo" \
	test "$(grep -c 'path: /catalog/products/p3$' "$yaml")" = 1

# --- 6. troca de porta a quente ----------------------------------------------

section "6. troca de porta a quente (PATCH /api/settings)"

started=$(api /api/status | fields startedAt)
patch() { # patch url json
	curl -s -o "$work/b" -w '%{http_code}' -X PATCH "$1/api/settings" \
		-H 'Content-Type: application/merge-patch+json' -d "$2"
}
check "tráfego 8080 → 9080 aceito (200)" test "$(patch "$A" '{"ports":{"traffic":9080}}')" = 200
check "gateway.json regravado com a porta nova" grep -q '"traffic": 9080' "$work/cfg/gateway.json"
check "a 9080 atende o tráfego" test "$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:9080/catalog/products)" = 200
check "a 8080 deixou de aceitar conexões" port_free 8080
check "administração 8081 → 9081 aceita (200)" test "$(patch "$A" '{"ports":{"admin":9081}}')" = 200
check "a API responde na 9081" \
	bash -c "curl -s http://127.0.0.1:9081/api/status | grep -q '\"admin\": 9081'"
check "a 8081 deixou de aceitar conexões" port_free 8081
check "mesmo processo, sem reinício (startedAt igual)" \
	test "$(curl -s http://127.0.0.1:9081/api/status | fields startedAt)" = "$started"
check "volta às portas 8080 e 8081 (200)" \
	test "$(patch http://127.0.0.1:9081 '{"ports":{"traffic":8080,"admin":8081}}')" = 200
check "a 8080 volta a atender" test "$(curl -s -o /dev/null -w '%{http_code}' "$T/catalog/products")" = 200
check "a 8081 volta a atender" bash -c "curl -s '$A/api/status' | grep -q '\"traffic\": 8080'"
check "a 9080 e a 9081 foram fechadas" bash -c "! curl -s -o /dev/null --max-time 1 http://127.0.0.1:9080/ && ! curl -s -o /dev/null --max-time 1 http://127.0.0.1:9081/"

# --- resumo ------------------------------------------------------------------

printf '\n%d verificações passaram, %d falharam\n' "$passed" "$failed"
[[ $failed -eq 0 ]]
