#!/usr/bin/env bash
# Sobe o ambiente de exemplo: os dois serviços de brinquedo e o gateway na
# frente deles. Ctrl+C encerra tudo.
#
#   examples/run.sh            sobe tudo reaproveitando examples/.run/, com o
#                              que o aprendizado e o painel gravaram antes;
#                              só cria a cópia quando ela ainda não existe
#   CLEAN=1 examples/run.sh    descarta examples/.run/ e recomeça do exemplo
#                              original (apaga serviços e regras gravados ali)
#   WEB=1 examples/run.sh      constrói o painel (web/dist) antes do gateway;
#                              sem ele, o binário serve o placeholder
#
# Portas: gateway 8080 (tráfego) e 8081 (administração e painel), payments
# 9001, catalog 9002. Os toys aceitam PAYMENTS_ADDR, CATALOG_ADDR e
# CATALOG_FAIL; mudar os endereços exige ajustar o upstream das rotas.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
work="$here/.run"
exe="$(cd "$root" && go env GOEXE)"

# Por padrão a cópia de trabalho é preservada: ela guarda os serviços e as
# regras que o usuário criou pelo painel e o que o aprendizado gravou.
if [[ "${CLEAN:-}" == 1 || ! -f "$work/gateway.json" ]]; then
	if [[ -d "$work" ]]; then
		backup="$work.bak-$(date +%Y%m%d-%H%M%S)"
		echo "guardando a configuração anterior em $backup"
		mv "$work" "$backup"
	fi
	mkdir -p "$work"
	cp "$here/gateway.json" "$work/"
	cp -r "$here/routes" "$work/routes"
fi
mkdir -p "$work/bin"

if [[ "${WEB:-}" == 1 ]]; then
	echo "construindo o painel..."
	(cd "$root/web" && npm ci && npm run build)
fi

echo "compilando..."
(cd "$root" &&
	go build -o "$work/bin/gateway$exe" ./cmd/gateway &&
	go build -o "$work/bin/payments$exe" ./examples/toys/payments &&
	go build -o "$work/bin/catalog$exe" ./examples/toys/catalog)

payments_addr="${PAYMENTS_ADDR:-127.0.0.1:9001}"
catalog_addr="${CATALOG_ADDR:-127.0.0.1:9002}"

# No Git Bash (MSYS), o kill do shell não chega ao processo nativo do
# Windows: guarda também o PID do Windows para encerrar com taskkill.
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

# Espera o /health de um toy responder, para o gateway não subir antes dos
# upstreams e o primeiro acesso não sair como upstream indisponível.
ready() {
	command -v curl >/dev/null || { sleep 1; return; }
	for _ in $(seq 50); do
		curl -fs -o /dev/null "http://$1/health" && return
		sleep 0.2
	done
	echo "aviso: $1 não respondeu ao /health" >&2
}

start "$work/bin/payments$exe" -addr "$payments_addr"
start "$work/bin/catalog$exe" -addr "$catalog_addr" -fail "${CATALOG_FAIL:-0.25}"
ready "$payments_addr"
ready "$catalog_addr"
start "$work/bin/gateway$exe" -config "$work/gateway.json"

cat <<'EOF'

ambiente de exemplo no ar (Ctrl+C encerra):
  painel e API   http://localhost:8081
  tráfego        http://localhost:8080

  passthrough           curl -s localhost:8080/catalog/products
  upstream instável     curl -si localhost:8080/catalog/stock/p1
  override forçado      curl -si -X POST localhost:8080/payments/charges \
                          -H 'X-Scenario: declined' -d '{"amount":4990}'
  sem o override        curl -si -X POST localhost:8080/payments/charges -d '{"amount":4990}'
  probabilístico (30%)  for i in $(seq 10); do curl -s -o /dev/null -w '%{http_code} ' localhost:8080/payments/balance; done
  latência 200-900 ms   curl -s -w '\n%{time_total}s\n' localhost:8080/payments/charges/ch_1
  SSE                   curl -sN localhost:8080/payments/events
  aprendizado           curl -s localhost:8080/catalog/search?q=caneca; cat examples/.run/routes/catalog.yaml
  troca de porta        curl -s -X PATCH localhost:8081/api/settings \
                          -H 'Content-Type: application/json' -d '{"ports":{"traffic":9080}}'

EOF

# Encerra quando qualquer um dos processos sair (ou no Ctrl+C).
wait -n "${pids[@]}"
