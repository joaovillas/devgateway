# API de administração

Contrato da API REST servida na porta de administração (padrão `8081`), sob o prefixo `/api/`. O backend a implementa na fase 7 de `openspec/changes/add-test-gateway/tasks.md`. O painel em `web/` é um cliente dela e nada mais: toda operação que o painel faz tem aqui um equivalente executável por `curl`.

Os campos JSON são os dos tipos Go em `internal/config` (`Route`, `Override`, `EffectiveValue`) e em `internal/exchange` (`Exchange`, `Filter`). Quando este documento e o código divergirem, o código dos tipos manda e este documento é corrigido.

Nos exemplos, `$A` é `http://localhost:8081`.

## Sumário

| Grupo | Método e path | Operação |
|---|---|---|
| Processo | `GET /api/status` | estado resumido do processo |
| Rotas | `GET /api/routes` | listar rotas |
| | `GET /api/routes/{route}` | ler uma rota |
| | `POST /api/routes` | criar rota |
| | `PUT /api/routes/{route}` | substituir rota |
| | `PATCH /api/routes/{route}` | alterar campos da rota |
| | `DELETE /api/routes/{route}` | remover rota e seu documento |
| | `GET /api/routes/{route}/document` | ler o YAML bruto |
| | `PUT /api/routes/{route}/document` | gravar o YAML bruto |
| Overrides | `GET /api/routes/{route}/overrides` | listar overrides da rota |
| | `GET /api/routes/{route}/overrides/{override}` | ler um override |
| | `POST /api/routes/{route}/overrides` | criar override |
| | `PUT /api/routes/{route}/overrides/{override}` | substituir override |
| | `PATCH /api/routes/{route}/overrides/{override}` | alterar campos (inclui liga/desliga) |
| | `DELETE /api/routes/{route}/overrides/{override}` | remover override |
| | `POST /api/routes/{route}/overrides/{override}/reset` | reiniciar TTL e contagem |
| | `POST /api/routes/{route}/overrides/derive` | derivar override de uma troca |
| | `GET /api/overrides/state` | estado vivo de todos os overrides |
| Configuração | `GET /api/settings` | configuração efetiva com origem |
| | `PATCH /api/settings` | alterar `gateway.json` e aplicar a quente |
| | `GET /api/settings/document` | ler `gateway.json` bruto |
| | `PUT /api/settings/document` | gravar `gateway.json` bruto |
| | `GET /api/learning` | estado do modo aprendizado |
| | `PUT /api/learning` | ligar ou desligar o aprendizado |
| | `POST /api/reload` | recarregar arquivos do disco |
| Histórico | `GET /api/exchanges` | listar trocas com filtros e cursor |
| | `GET /api/exchanges/{id}` | ler uma troca completa |
| | `GET /api/exchanges/{id}/older` | troca anterior (mais antiga) |
| | `GET /api/exchanges/{id}/newer` | troca seguinte (mais nova) |
| | `DELETE /api/exchanges` | limpar o histórico |
| Upstreams | `GET /api/upstreams` | disponibilidade recente por upstream |
| Tempo real | `GET /api/events` | fluxo SSE |

## Convenções

### Formato

- Corpo de requisição e de resposta em `application/json; charset=utf-8`, exceto os documentos brutos: `application/yaml` para rotas e `application/json` para `gateway.json`.
- Chaves em camelCase, iguais às dos documentos.
- Durações são texto no formato do Go: `"150ms"`, `"2s"`, `"1m30s"`.
- Instantes são RFC 3339 com fração, em UTC: `"2026-09-18T15:04:05.123Z"`.
- Tempos medidos (`timing`, `ttlRemainingMs`) são números em milissegundos.
- Nomes de rota e de override aparecem no path como estão, com percent-encoding quando necessário.
- Corpos capturados (`request.body`, `response.body` de uma troca) são `[]byte` no Go e, portanto, chegam em **base64**. O cliente decodifica e usa `headers["Content-Type"]` para decidir como exibir. O campo `size` guarda o tamanho real, e `truncated` indica corte no limite de captura.

### Erros

Toda resposta de erro tem este corpo:

```json
{
  "error": "invalid",
  "message": "routes/payments.yaml: campo overrides[0].probability (linha 12, coluna 18): deve estar entre 0 e 1 (recebido 1.5)",
  "field": "overrides[0].probability",
  "file": "routes/payments.yaml",
  "line": 12,
  "column": 18
}
```

| Campo | Presença | Significado |
|---|---|---|
| `error` | sempre | código estável, para o cliente decidir o que fazer |
| `message` | sempre | texto em pt-BR para exibir como está |
| `field` | quando há campo responsável | caminho do campo (`overrides[0].latency.min`, `ports.traffic`) |
| `file` | quando há documento responsável | caminho do arquivo, ou `variável de ambiente X` |
| `line`, `column` | quando o campo foi localizado no documento | posição 1-based |
| `env` | só em `locked` | variável de ambiente que trava o valor |
| `errors` | quando há mais de um problema | lista de objetos com `message`, `field`, `file`, `line`, `column` |

Códigos usados:

| HTTP | `error` | Quando |
|---|---|---|
| 400 | `bad_request` | JSON malformado, parâmetro de query inválido, corpo ausente |
| 400 | `bad_cursor` | cursor de paginação não emitido por este backend |
| 403 | `history_disabled` | exposição do histórico desligada (`history.expose = false`) |
| 404 | `not_found` | rota, override, troca ou recurso da API inexistente |
| 404 | `no_more` | navegação item a item chegou ao fim naquela direção |
| 405 | `method_not_allowed` | método não suportado no path |
| 409 | `conflict` | nome já usado, ou casamento idêntico ao de outra rota |
| 409 | `locked` | valor definido por variável de ambiente |
| 409 | `port_unavailable` | porta nova não pôde ser aberta; a atual segue em uso |
| 409 | `backend_unavailable` | backend novo do histórico não inicializou; o atual segue em uso |
| 412 | `stale` | `If-Match` não confere com a versão atual do documento |
| 415 | `unsupported_media_type` | `Content-Type` diferente do esperado |
| 422 | `invalid` | falha de validação; nada foi gravado |
| 500 | `internal` | falha inesperada, inclusive de escrita em disco |

`history_disabled` é deliberadamente distinto de uma lista vazia: o painel mostra "histórico desabilitado" num caso e "nenhuma troca ainda" no outro.

### Escrita e documentos

- Toda escrita valida primeiro. Se a validação falha, a resposta é `422 invalid` e nenhum arquivo é tocado.
- Uma escrita de rota ou override reescreve **somente** o documento daquela rota, de forma atômica (arquivo temporário e rename), sob um mutex de escrita. Os demais documentos ficam byte a byte iguais.
- A reescrita perde os comentários e a ordem das chaves do documento tocado. Por isso a leitura de rota informa `hasComments`, e o painel avisa antes da primeira escrita.
- Cada documento tem uma versão opaca devolvida no cabeçalho `ETag`. Toda escrita aceita `If-Match` opcional. Com ele, uma versão divergente responde `412 stale` sem gravar. Sem ele, a última escrita vence.
- Toda escrita bem-sucedida reconstrói o snapshot, aplica a configuração a quente e emite um evento `config` no fluxo SSE.

---

## Processo

### `GET /api/status`

Estado resumido, usado pela barra superior do painel.

```json
{
  "version": "0.1.0",
  "schemaVersion": 1,
  "startedAt": "2026-09-18T15:00:00Z",
  "configPath": "gateway.json",
  "routesDir": "routes",
  "ports": { "traffic": 8080, "admin": 8081 },
  "history": { "backend": "memory", "record": true, "expose": true },
  "learning": { "enabled": false },
  "routes": 3
}
```

```sh
curl -s $A/api/status
```

---

## Rotas

### O recurso rota

A leitura de uma rota embrulha o documento (`route`, idêntico ao tipo `config.Route`) com metadados que não pertencem ao documento.

```json
{
  "file": "routes/payments.yaml",
  "hasComments": true,
  "order": 0,
  "route": {
    "schemaVersion": 1,
    "name": "payments",
    "upstream": "http://localhost:9001",
    "match": { "host": "", "path": "/api/payments/*" },
    "stripPrefix": false,
    "rewriteHost": false,
    "timeout": "5s",
    "overrides": [
      {
        "name": "flaky",
        "enabled": true,
        "match": { "path": "/api/payments/charge", "method": "POST" },
        "respond": {
          "status": 503,
          "headers": { "Retry-After": "1" },
          "body": { "error": "indisponível" }
        },
        "probability": 0.3,
        "latency": { "min": "100ms", "max": "500ms" },
        "ttl": "10m",
        "maxApplications": 50
      }
    ]
  },
  "state": {
    "flaky": {
      "active": true,
      "expired": null,
      "registeredAt": "2026-09-18T15:02:00Z",
      "ttlRemainingMs": 412000,
      "applications": 7,
      "maxApplications": 50,
      "lastAppliedAt": "2026-09-18T15:05:10.200Z"
    }
  }
}
```

- `order` é a posição da rota na precedência (0 é a mais específica).
- `state` é o estado vivo de cada override, pelo nome (ver [estado vivo](#get-apioverridesstate)).
- Campos omitidos seguem as regras do documento: `enabled` ausente equivale a ligado, `probability` ausente a `1.0`, `respond.status` ausente a `200`.
- `latency` é texto (`"2s"`, atraso fixo) ou `{ "min", "max" }` (intervalo sorteado).
- Um critério de `headers`, `query` ou `body` é texto (igualdade) ou um objeto com exatamente um de `equals`, `regex`, `json`, `contains`.
- `source` aparece nos overrides aprendidos ou derivados: `{ "kind": "learned" | "derived", "exchange": "<id>", "at": "<instante>", "bodyIncomplete": true }`.

### `GET /api/routes`

Lista as rotas na ordem de precedência.

```json
{ "items": [ { "file": "routes/payments.yaml", "hasComments": false, "order": 0, "route": { "...": "..." }, "state": { } } ] }
```

Sem rotas, `items` é `[]`.

```sh
curl -s $A/api/routes
```

### `GET /api/routes/{route}`

Devolve o recurso rota e o cabeçalho `ETag` do documento.

Erros: `404 not_found`.

```sh
curl -si $A/api/routes/payments
```

### `POST /api/routes`

Cria a rota e o documento `routes/{name}.yaml`. O corpo é um `config.Route`. `schemaVersion` ausente assume a versão do binário.

```json
{
  "name": "orders",
  "upstream": "http://localhost:9002",
  "match": { "path": "/api/orders/*" },
  "timeout": "3s"
}
```

Resposta `201 Created` com o recurso rota, `Location: /api/routes/orders` e `ETag`.

Erros: `400 bad_request`, `409 conflict` (nome já usado, ou mesmo host e path de outra rota; a mensagem nomeia o arquivo em conflito), `422 invalid`.

```sh
curl -s -X POST $A/api/routes -H 'Content-Type: application/json' \
  -d '{"name":"orders","upstream":"http://localhost:9002","match":{"path":"/api/orders/*"},"timeout":"3s"}'
```

### `PUT /api/routes/{route}`

Substitui a rota inteira, overrides incluídos. Um `name` diferente do path renomeia a rota, e o documento mantém o caminho de arquivo atual. Overrides mantidos com o mesmo nome preservam o estado vivo (TTL e contagem).

Resposta `200` com o recurso rota. Erros: `400`, `404 not_found`, `409 conflict`, `412 stale`, `422 invalid`.

```sh
curl -s -X PUT $A/api/routes/orders -H 'Content-Type: application/json' \
  -d '{"schemaVersion":1,"name":"orders","upstream":"http://localhost:9002","match":{"path":"/api/orders/*"},"timeout":"5s"}'
```

### `PATCH /api/routes/{route}`

Altera campos da rota com JSON Merge Patch (RFC 7396): as chaves presentes substituem as atuais e `null` remove o campo do documento. `overrides` não é aceito aqui (`422`), porque os overrides têm endpoints próprios.

```json
{ "upstream": "http://localhost:9003", "timeout": null }
```

Resposta `200` com o recurso rota. Erros: `400`, `404`, `409 conflict`, `412`, `422`.

```sh
curl -s -X PATCH $A/api/routes/orders -H 'Content-Type: application/merge-patch+json' \
  -d '{"upstream":"http://localhost:9003","timeout":null}'
```

### `DELETE /api/routes/{route}`

Remove a rota e apaga o documento. Resposta `204`. Erros: `404`, `412`.

```sh
curl -s -X DELETE $A/api/routes/orders
```

### `GET /api/routes/{route}/document`

Devolve o documento YAML exatamente como está em disco, comentários incluídos, com `Content-Type: application/yaml; charset=utf-8` e `ETag`.

```yaml
# Pagamentos: o serviço local da equipe de billing
schemaVersion: 1
name: payments
upstream: http://localhost:9001
match:
  path: /api/payments/*
overrides:
  - name: flaky
    match:
      path: /api/payments/charge
      method: POST
    respond:
      status: 503
    probability: 0.3
```

Erros: `404`.

```sh
curl -s $A/api/routes/payments/document
```

### `PUT /api/routes/{route}/document`

Grava o documento bruto. O corpo é YAML (`Content-Type: application/yaml`). O texto é validado como na carga do disco e gravado **como enviado**, comentários incluídos. Se a rota não existe, é criada (`201`). O `name` declarado precisa ser igual ao `{route}` do path, senão `422` com `field: "name"`.

Resposta `200` ou `201` com o recurso rota e o novo `ETag`. Erros: `409 conflict`, `412 stale`, `415`, `422 invalid` com `file`, `field`, `line` e `column` apontando o problema.

```sh
curl -s -X PUT $A/api/routes/payments/document -H 'Content-Type: application/yaml' \
  --data-binary @routes/payments.yaml
```

---

## Overrides

### O recurso override

```json
{
  "route": "payments",
  "order": 0,
  "override": {
    "name": "flaky",
    "enabled": true,
    "match": { "path": "/api/payments/charge", "method": "POST" },
    "respond": { "status": 503 },
    "probability": 0.3,
    "latency": "2s",
    "drop": false,
    "ttl": "60s",
    "maxApplications": 5
  },
  "state": {
    "active": true,
    "expired": null,
    "registeredAt": "2026-09-18T15:02:00Z",
    "ttlRemainingMs": 40000,
    "applications": 2,
    "maxApplications": 5,
    "lastAppliedAt": "2026-09-18T15:02:19.800Z"
  }
}
```

`override` é o tipo `config.Override`. `order` é a posição do override na precedência dentro da rota. `state` é descrito em [estado vivo](#get-apioverridesstate).

### `GET /api/routes/{route}/overrides`

```json
{ "items": [ { "route": "payments", "order": 0, "override": { "...": "..." }, "state": { "...": "..." } } ] }
```

Erros: `404` (rota).

```sh
curl -s $A/api/routes/payments/overrides
```

### `GET /api/routes/{route}/overrides/{override}`

Erros: `404` (rota ou override).

```sh
curl -s $A/api/routes/payments/overrides/flaky
```

### `POST /api/routes/{route}/overrides`

Acrescenta um override ao fim da lista declarada da rota. O corpo é um `config.Override`. Resposta `201` com o recurso override. Erros: `404` (rota), `409 conflict` (nome repetido na rota), `412`, `422`.

```sh
curl -s -X POST $A/api/routes/payments/overrides -H 'Content-Type: application/json' \
  -d '{"name":"flaky","match":{"path":"/api/payments/charge","method":"POST"},"respond":{"status":503},"probability":0.3}'
```

### `PUT /api/routes/{route}/overrides/{override}`

Substitui o override inteiro, mantendo sua posição na lista declarada. Um `name` diferente renomeia. Resposta `200`. Erros: `404`, `409`, `412`, `422`.

```sh
curl -s -X PUT $A/api/routes/payments/overrides/flaky -H 'Content-Type: application/json' \
  -d '{"name":"flaky","match":{"path":"/api/payments/charge"},"respond":{"status":500},"probability":0.5}'
```

### `PATCH /api/routes/{route}/overrides/{override}`

JSON Merge Patch sobre o override. É o endpoint dos controles contínuos e do liga/desliga. `null` remove o campo (por exemplo, `"latency": null` tira o atraso).

A API não liga o override por conta própria. A spec pede que ajustar probabilidade, latência ou queda de um override desligado o ligue no mesmo gesto, e quem cumpre isso é o cliente, mandando `"enabled": true` junto:

```json
{ "probability": 0.3, "enabled": true }
```

Liga/desliga isolado:

```json
{ "enabled": false }
```

Desligar e religar preserva todos os demais campos. Resposta `200` com o recurso override. Erros: `400`, `404`, `412`, `422`.

```sh
curl -s -X PATCH $A/api/routes/payments/overrides/flaky -H 'Content-Type: application/merge-patch+json' \
  -d '{"probability":0.3,"enabled":true}'
curl -s -X PATCH $A/api/routes/payments/overrides/flaky -H 'Content-Type: application/merge-patch+json' \
  -d '{"enabled":false}'
```

### `DELETE /api/routes/{route}/overrides/{override}`

Resposta `204`. Erros: `404`, `412`.

```sh
curl -s -X DELETE $A/api/routes/payments/overrides/flaky
```

### `POST /api/routes/{route}/overrides/{override}/reset`

Reinicia o relógio do TTL e zera a contagem de aplicações, reativando um override expirado. Não altera o documento. Resposta `200` com o recurso override. Erros: `404`.

```sh
curl -s -X POST $A/api/routes/payments/overrides/flaky/reset
```

### `POST /api/routes/{route}/overrides/derive`

Monta um override a partir de uma troca do histórico: critério de path exato e método da requisição observada, e resposta com status, cabeçalhos e corpo devolvidos pelo upstream (sem `Date`, `Content-Length` e cabeçalhos hop-by-hop). `source.kind` é `derived`.

Corpo:

```json
{ "exchange": "01K5E3V3C8Q2M4Z8N6P0R2T4W6", "name": "charge-ok", "save": false }
```

- `name` é opcional. Sem ele, o nome é gerado a partir de método e path (`post-api-payments-charge`).
- `save: false` (padrão) devolve o rascunho com `200`, sem gravar nada. É a etapa de revisão: o painel mostra o rascunho, o usuário edita e cria com `POST /api/routes/{route}/overrides`.
- `save: true` grava direto e responde `201`, como o `POST` de criação.

Resposta (rascunho):

```json
{
  "route": "payments",
  "override": {
    "name": "charge-ok",
    "match": { "path": "/api/payments/charge", "method": "POST" },
    "respond": {
      "status": 200,
      "headers": { "Content-Type": "application/json" },
      "body": { "id": "ch_1", "status": "paid" }
    },
    "source": {
      "kind": "derived",
      "exchange": "01K5E3V3C8Q2M4Z8N6P0R2T4W6",
      "at": "2026-09-18T15:10:00Z",
      "bodyIncomplete": false
    }
  },
  "warnings": []
}
```

Um corpo JSON válido vira estrutura em `respond.body`. Qualquer outro corpo vira texto. Se a troca foi truncada na captura, o rascunho sai com `source.bodyIncomplete: true` e um aviso em `warnings`. A derivação não é recusada, mas o painel mostra o aviso antes de gravar.

Erros: `403 history_disabled`, `404 not_found` (rota, ou troca inexistente: `"message": "troca 01K5... não encontrada no histórico"`), `422` (troca sem resposta do upstream: sintetizada, derrubada ou erro do gateway).

```sh
curl -s -X POST $A/api/routes/payments/overrides/derive -H 'Content-Type: application/json' \
  -d '{"exchange":"01K5E3V3C8Q2M4Z8N6P0R2T4W6","name":"charge-ok"}'
```

### `GET /api/overrides/state`

Estado vivo de todos os overrides de todas as rotas, para o mapa e os contadores do painel.

```json
{
  "now": "2026-09-18T15:02:20Z",
  "items": [
    {
      "route": "payments",
      "override": "flaky",
      "enabled": true,
      "active": true,
      "expired": null,
      "registeredAt": "2026-09-18T15:02:00Z",
      "ttlRemainingMs": 40000,
      "applications": 2,
      "maxApplications": 5,
      "lastAppliedAt": "2026-09-18T15:02:19.800Z"
    },
    {
      "route": "payments",
      "override": "charge-learned",
      "enabled": false,
      "active": false,
      "expired": null,
      "registeredAt": "2026-09-18T14:50:00Z",
      "ttlRemainingMs": null,
      "applications": 0,
      "maxApplications": null,
      "lastAppliedAt": null
    }
  ]
}
```

| Campo | Significado |
|---|---|
| `enabled` | valor do documento |
| `active` | participa da seleção agora: ligado e não expirado |
| `expired` | `null`, `"ttl"` ou `"applications"` |
| `registeredAt` | início do relógio do TTL: criação, última alteração de `ttl` ou `maxApplications`, religação ou `reset` |
| `ttlRemainingMs` | restante do TTL, `null` sem TTL, `0` expirado |
| `applications` | aplicações desde `registeredAt` |
| `maxApplications` | limite declarado, `null` sem limite |
| `lastAppliedAt` | última aplicação, `null` se nunca |

O painel decrementa `ttlRemainingMs` localmente entre eventos, a partir de `now`.

```sh
curl -s $A/api/overrides/state
```

---

## Configuração do processo

### `GET /api/settings`

Configuração efetiva com a origem de cada valor, na ordem de `Settings.Effective()`. Cada item é um `config.EffectiveValue` acrescido de `locked`.

```json
{
  "file": { "path": "gateway.json", "exists": true },
  "values": [
    { "key": "ports.traffic", "env": "GATEWAY_TRAFFIC_PORT", "value": 9090, "source": { "origin": "env", "name": "GATEWAY_TRAFFIC_PORT" }, "locked": true },
    { "key": "ports.admin", "env": "GATEWAY_ADMIN_PORT", "value": 8081, "source": { "origin": "default" }, "locked": false },
    { "key": "seed", "env": "GATEWAY_SEED", "value": 42, "source": { "origin": "file", "name": "gateway.json" }, "locked": false },
    { "key": "history.backend", "env": "GATEWAY_HISTORY_BACKEND", "value": "memory", "source": { "origin": "default" }, "locked": false },
    { "key": "history.path", "env": "GATEWAY_HISTORY_PATH", "value": "", "source": { "origin": "default" }, "locked": false },
    { "key": "history.capacity", "env": "GATEWAY_HISTORY_CAPACITY", "value": 1000, "source": { "origin": "default" }, "locked": false },
    { "key": "history.record", "env": "GATEWAY_HISTORY_RECORD", "value": true, "source": { "origin": "default" }, "locked": false },
    { "key": "history.expose", "env": "GATEWAY_HISTORY_EXPOSE", "value": true, "source": { "origin": "default" }, "locked": false },
    { "key": "capture.maxBodyBytes", "env": "GATEWAY_CAPTURE_MAX_BODY_BYTES", "value": 65536, "source": { "origin": "default" }, "locked": false },
    { "key": "learning.enabled", "env": "GATEWAY_LEARNING", "value": false, "source": { "origin": "default" }, "locked": false },
    { "key": "routesDir", "env": "GATEWAY_ROUTES_DIR", "value": "routes", "source": { "origin": "default" }, "locked": false }
  ]
}
```

- `source.origin` é `env`, `file` ou `default`. `source.name` é a variável ou o arquivo.
- `locked` é verdadeiro quando `origin` é `env`: a API recusa alterar esse valor.
- `seed` com `value: null` significa aleatoriedade não reproduzível.

```sh
curl -s $A/api/settings
```

### `PATCH /api/settings`

Altera `gateway.json` com JSON Merge Patch sobre o formato do arquivo (`config.GatewayFile`) e aplica o resultado a quente, sem reiniciar. `null` remove a chave do arquivo, e o valor volta ao padrão.

```json
{ "seed": 42, "ports": { "traffic": 9090 }, "history": { "backend": "sqlite", "path": "data/history.db" } }
```

Ordem de aplicação:

1. Recusa com `409 locked` se alguma chave tocada vem do ambiente. Nada é gravado.
2. Valida o resultado (`422 invalid`, por exemplo portas iguais).
3. Se a porta de tráfego ou de administração muda, abre o listener novo. Se falhar, responde `409 port_unavailable` e nada muda. Se abrir, o servidor antigo recebe `Shutdown` e as requisições em curso terminam nele.
4. Se o backend do histórico muda, inicializa o novo. Se falhar, responde `409 backend_unavailable` e nada muda. Se inicializar, troca o ponteiro e fecha o antigo. **O histórico não é migrado.**
5. Grava `gateway.json` de forma atômica e troca a configuração.

Resposta `200`:

```json
{
  "settings": { "file": { "path": "gateway.json", "exists": true }, "values": [ "..." ] },
  "applied": ["seed", "ports.traffic", "history.backend", "history.path"],
  "notes": [
    "porta de tráfego agora é 9090; a 8080 deixou de aceitar conexões",
    "histórico agora em sqlite (data/history.db); as trocas anteriores continuam no backend memory e não foram migradas"
  ]
}
```

Com mudança da porta de administração, a resposta sai pela porta antiga e só depois ela fecha. O cliente usa `ports.admin` da resposta para se reconectar.

Erros:

```json
{
  "error": "locked",
  "message": "ports.traffic vem da variável de ambiente GATEWAY_TRAFFIC_PORT e não pode ser alterado pela API; gateway.json não foi modificado",
  "field": "ports.traffic",
  "env": "GATEWAY_TRAFFIC_PORT"
}
```

```json
{
  "error": "port_unavailable",
  "message": "porta 9090 indisponível: bind: address already in use; o tráfego segue na 8080",
  "field": "ports.traffic"
}
```

```json
{
  "error": "backend_unavailable",
  "message": "backend sqlite não inicializou em /ro/history.db: permission denied; o histórico segue em memory",
  "field": "history.backend"
}
```

```sh
curl -s -X PATCH $A/api/settings -H 'Content-Type: application/merge-patch+json' -d '{"seed":42}'
curl -s -X PATCH $A/api/settings -H 'Content-Type: application/merge-patch+json' -d '{"ports":{"traffic":9090}}'
curl -s -X PATCH $A/api/settings -H 'Content-Type: application/merge-patch+json' \
  -d '{"history":{"backend":"sqlite","path":"data/history.db"}}'
```

### `GET /api/settings/document`

Devolve `gateway.json` como está em disco, com `ETag`. Se o arquivo não existe, responde `200` com `{}` e o cabeçalho `X-Gateway-File-Exists: false`.

```sh
curl -s $A/api/settings/document
```

### `PUT /api/settings/document`

Grava `gateway.json` bruto (`Content-Type: application/json`) e aplica a quente, com a mesma ordem e os mesmos erros de `PATCH /api/settings`. Um documento que altera uma chave travada pelo ambiente é recusado com `409 locked`. Um documento que só repete o valor atual da chave travada é aceito. O texto é gravado como enviado. Erros adicionais: `412 stale`, `415`, `422` com `line` e `column` para JSON inválido.

```sh
curl -s -X PUT $A/api/settings/document -H 'Content-Type: application/json' --data-binary @gateway.json
```

### `GET /api/learning`

```json
{
  "enabled": false,
  "source": { "origin": "default" },
  "locked": false,
  "learned": { "payments": 3, "orders": 0 }
}
```

`learned` conta, por rota, os overrides com `source.kind = "learned"`. É a contagem que o painel mostra para o usuário consolidar em curinga.

```sh
curl -s $A/api/learning
```

### `PUT /api/learning`

Atalho para `PATCH /api/settings` com `{"learning":{"enabled":...}}`, gravado em `gateway.json`.

```json
{ "enabled": true }
```

Resposta `200` com o mesmo corpo de `GET /api/learning`. Erros: `409 locked` (`env: "GATEWAY_LEARNING"`), `422`.

```sh
curl -s -X PUT $A/api/learning -H 'Content-Type: application/json' -d '{"enabled":true}'
```

### `POST /api/reload`

Relê `gateway.json` e o diretório de rotas e aplica a quente, sob as mesmas regras de `PATCH /api/settings` para portas e backend. Se algo falha, a configuração anterior continua em vigor.

Resposta `200`:

```json
{
  "routes": 4,
  "changed": { "routes": ["orders"], "settings": ["seed"] },
  "warnings": ["routes/README.md ignorado: extensão não reconhecida"]
}
```

Erros: `409 port_unavailable`, `409 backend_unavailable`, `409 conflict` (colisão entre documentos, nomeando os dois arquivos), `422 invalid` (com `errors` quando há vários problemas).

```sh
curl -s -X POST $A/api/reload
```

---

## Histórico

Com `history.expose = false`, todos os endpoints de leitura desta seção respondem `403 history_disabled`:

```json
{
  "error": "history_disabled",
  "message": "o histórico está desabilitado: history.expose = false (origem: variável de ambiente GATEWAY_HISTORY_EXPOSE)",
  "field": "history.expose",
  "env": "GATEWAY_HISTORY_EXPOSE"
}
```

`env` aparece quando o valor vem do ambiente e `file` quando vem de `gateway.json`.

### O recurso troca

É o tipo `exchange.Exchange`:

```json
{
  "id": "01K5E3V3C8Q2M4Z8N6P0R2T4W6",
  "seq": 128,
  "start": "2026-09-18T15:05:10.050Z",
  "method": "POST",
  "host": "localhost:8080",
  "path": "/api/payments/charge",
  "query": "retry=1",
  "clientAddr": "127.0.0.1:53122",
  "route": "payments",
  "upstream": "http://localhost:9001",
  "override": "payments/flaky",
  "interventions": ["synthesized", "delayed"],
  "outcome": "synthesized",
  "status": 503,
  "request": {
    "headers": { "Content-Type": ["application/json"] },
    "body": "eyJhbW91bnQiOjEwMH0=",
    "size": 14
  },
  "response": {
    "headers": { "Content-Type": ["application/json"], "X-Gateway": ["route=payments; override=payments/flaky; intervention=synthesized"] },
    "body": "eyJlcnJvciI6ImluZGlzcG9uw612ZWwifQ==",
    "size": 26,
    "truncated": false
  },
  "timing": { "totalMs": 2001.4, "upstreamMs": 0, "injectedMs": 2000, "gatewayMs": 1.4 }
}
```

- `outcome`: `upstream`, `synthesized`, `dropped` ou `gateway` (erro do próprio gateway: `404` sem rota, `502`, `504`, `501`).
- `interventions`: o que o override fez (`synthesized`, `delayed`, `dropped`). Vazio ou ausente quando não houve intervenção.
- `dropMode`: `hijack` (HTTP/1.1) ou `stream_reset` (HTTP/2), só em quedas.
- `error`: texto do erro do upstream ou do gateway, quando houve.
- `status`: zero numa queda.
- `headers`: `http.Header`, ou seja, cada nome aponta uma lista de valores.
- `body`: base64. Ausente na listagem, que devolve o resumo sem corpos.

### `GET /api/exchanges`

Lista em ordem cronológica inversa, da troca mais nova para a mais antiga, sem os corpos.

Parâmetros de query (todos opcionais e conjuntivos, espelhando `exchange.Filter`):

| Parâmetro | Exemplo | Filtro |
|---|---|---|
| `route` | `payments` | rota casada, igualdade |
| `upstream` | `http://localhost:9001` | upstream, igualdade |
| `override` | `payments/flaky` | override responsável, igualdade |
| `method` | `POST` | método, sem diferenciar caixa |
| `path` | `/charge` | path contém o texto |
| `statusMin`, `statusMax` | `500`, `599` | faixa inclusiva de status |
| `intervened` | `true` ou `false` | com ou sem intervenção |
| `since`, `until` | RFC 3339 | janela `[since, until)` |
| `limit` | `50` | tamanho da página, padrão 50 e máximo 500 |
| `cursor` | valor de `next` | continuação da página anterior |

Resposta `200`:

```json
{
  "items": [ { "id": "01K5E3V3C8Q2M4Z8N6P0R2T4W6", "seq": 128, "...": "resumo sem body" } ],
  "next": "eyJzZXEiOjc4fQ",
  "recording": true,
  "backend": "memory"
}
```

- `next` vazio indica que não há mais páginas.
- `recording: false` indica que o registro está desligado (`history.record = false`). A lista pode estar vazia por isso, e o painel diz isso.

Erros: `400 bad_request` (parâmetro inválido, com `field`), `400 bad_cursor`, `403 history_disabled`.

```sh
curl -s "$A/api/exchanges?route=payments&statusMin=500&statusMax=599&intervened=true&limit=50"
curl -s "$A/api/exchanges?cursor=eyJzZXEiOjc4fQ"
```

### `GET /api/exchanges/{id}`

Troca completa, com corpos. Erros: `403`, `404 not_found`.

```sh
curl -s $A/api/exchanges/01K5E3V3C8Q2M4Z8N6P0R2T4W6
```

### `GET /api/exchanges/{id}/older` e `GET /api/exchanges/{id}/newer`

Navegação item a item: devolve a troca completa imediatamente mais antiga (`older`) ou mais nova (`newer`) que `{id}`, entre as que satisfazem os filtros da query. Os filtros são os mesmos de `GET /api/exchanges`, exceto `limit` e `cursor`. A troca `{id}` não precisa satisfazer o filtro.

Erros: `403`, `404 not_found` (`{id}` inexistente), `404 no_more` (fim do histórico naquela direção, com `"message": "não há trocas mais antigas com esse filtro"`).

```sh
curl -s "$A/api/exchanges/01K5E3V3C8Q2M4Z8N6P0R2T4W6/older?statusMin=500&statusMax=599"
curl -s "$A/api/exchanges/01K5E3V3C8Q2M4Z8N6P0R2T4W6/newer?route=payments"
```

### `DELETE /api/exchanges`

Esvazia o histórico no backend em uso. As trocas seguintes voltam a ser registradas normalmente. Funciona também com a exposição desligada, porque não devolve dados. Resposta `204` e evento `history` no SSE.

```sh
curl -s -X DELETE $A/api/exchanges
```

---

## Upstreams

### `GET /api/upstreams`

Disponibilidade recente de cada upstream, para o mapa. O gateway não sonda os upstreams: o estado vem das últimas tentativas de encaminhamento, contadas no caminho da requisição e independentes do registro do histórico.

```json
{
  "items": [
    {
      "upstream": "http://localhost:9001",
      "routes": ["payments"],
      "status": "up",
      "recent": { "attempts": 20, "failures": 0 },
      "lastSuccessAt": "2026-09-18T15:05:10.100Z",
      "lastFailureAt": null,
      "lastError": ""
    },
    {
      "upstream": "http://localhost:9002",
      "routes": ["orders", "orders-admin"],
      "status": "down",
      "recent": { "attempts": 5, "failures": 5 },
      "lastSuccessAt": "2026-09-18T14:40:00Z",
      "lastFailureAt": "2026-09-18T15:05:09Z",
      "lastError": "dial tcp 127.0.0.1:9002: connect: connection refused"
    }
  ]
}
```

- `status`: `up` (a última tentativa recebeu resposta), `down` (as três últimas tentativas falharam por conexão recusada, tempo limite de conexão ou `504`), `unknown` (nenhuma tentativa desde o início ou desde a última recarga que mudou o upstream).
- `recent` cobre as últimas 20 tentativas.
- Uma resposta `5xx` do próprio upstream **não** o torna `down`: ele respondeu.
- Rotas sem `upstream` não aparecem aqui.

```sh
curl -s $A/api/upstreams
```

---

## Tempo real

### `GET /api/events`

Fluxo `text/event-stream`. O painel mantém uma conexão aberta e reage aos eventos. Cada evento tem `event:` e `data:` com um JSON numa linha. O servidor não reenvia eventos perdidos: ao reconectar, recebe `hello` e o cliente recarrega por REST o que exibe.

| `event` | Quando | `data` |
|---|---|---|
| `hello` | logo ao conectar | o mesmo corpo de `GET /api/status` |
| `exchanges` | no máximo uma vez por segundo, se houve trocas novas | `{ "items": [resumo...], "dropped": 0 }` |
| `config` | após qualquer escrita, recarga ou aprendizado | `{ "cause": "api" \| "reload" \| "learning", "routes": ["payments"], "settings": ["seed"] }` |
| `overrides` | quando o estado vivo muda de forma não contínua (expirou, reativou, aplicou) | o mesmo corpo de `GET /api/overrides/state` |
| `upstreams` | quando o `status` de algum upstream muda | o mesmo corpo de `GET /api/upstreams` |
| `history` | limpeza ou troca de backend | `{ "cause": "cleared" \| "backend", "backend": "sqlite" }` |
| `heartbeat` | a cada 15 s | `{ "now": "2026-09-18T15:05:15Z" }` |

- `exchanges.items` vem na ordem de registro (mais antiga primeiro), sem corpos, no máximo 200 por evento. `dropped` conta as trocas do intervalo que ficaram de fora.
- Com a exposição do histórico desligada, `exchanges` não é emitido.
- `overrides` com `applications` mudando a cada requisição é agregado a no máximo um por segundo, como `exchanges`.
- O cliente considera a conexão perdida se passar 45 s sem nenhum evento, porque o `heartbeat` garante ao menos um a cada 15 s.

Exemplo do fluxo:

```text
event: hello
data: {"version":"0.1.0","schemaVersion":1,"ports":{"traffic":8080,"admin":8081},"history":{"backend":"memory","record":true,"expose":true},"learning":{"enabled":false},"routes":3}

event: exchanges
data: {"items":[{"id":"01K5E3V3C8Q2M4Z8N6P0R2T4W6","seq":128,"method":"POST","path":"/api/payments/charge","route":"payments","status":503,"outcome":"synthesized","timing":{"totalMs":2001.4,"upstreamMs":0,"injectedMs":2000,"gatewayMs":1.4}}],"dropped":0}

event: heartbeat
data: {"now":"2026-09-18T15:05:15Z"}
```

```sh
curl -sN $A/api/events
```

---

## Paridade com os arquivos

| No arquivo | Na API |
|---|---|
| criar `routes/x.yaml` | `POST /api/routes` ou `PUT /api/routes/x/document` |
| editar um campo da rota | `PATCH /api/routes/x` |
| editar o YAML à mão | `PUT /api/routes/x/document` |
| apagar `routes/x.yaml` | `DELETE /api/routes/x` |
| acrescentar, editar ou remover um override | `POST`, `PATCH` e `DELETE /api/routes/x/overrides[/y]` |
| `enabled: false` num override | `PATCH ... {"enabled": false}` |
| editar `gateway.json` | `PATCH /api/settings` ou `PUT /api/settings/document` |
| `learning.enabled` | `PUT /api/learning` |
| editar os arquivos fora do painel | `POST /api/reload` |
