# gateway

Gateway HTTP para ambientes de desenvolvimento. Fica na frente dos serviços que você roda localmente, encaminha o tráfego para cada um por path ou por host e deixa você **interferir nesse tráfego de propósito**: responder no lugar do serviço, falhar numa fração das chamadas, atrasar, derrubar a conexão. Tudo o que passa por ele fica registrado com o tempo decomposto entre o que o serviço levou e o que o gateway injetou.

É um binário Go único, sem dependências em tempo de execução, com o painel web embutido.

- **Porta de tráfego** (padrão `8080`): onde o seu cliente fala. O gateway é transparente: método, path, query, corpo, cabeçalhos e `Host` seguem como chegaram. Ele só acrescenta os `X-Forwarded-*` e um cabeçalho próprio, `X-Gateway`.
- **Porta de administração** (padrão `8081`): o painel web e a API REST que o painel usa. Tudo o que o painel faz tem um equivalente por `curl` em [`docs/api.md`](docs/api.md).

Sumário: [instalação](#instalação) · [primeiros passos](#primeiros-passos) · [`gateway.json`](#gatewayjson) · [documentos de rota](#documentos-de-rota) · [variáveis de ambiente](#variáveis-de-ambiente) · [o override](#o-override-mock-e-caos-são-a-mesma-coisa) · [modo aprendizado](#modo-aprendizado) · [reconfiguração a quente](#reconfiguração-a-quente) · [paridade](#paridade-entre-arquivo-api-e-painel) · [limites assumidos](#limites-assumidos) · [desenvolvimento](#desenvolvimento)

## Instalação

### Binário

Precisa de Go 1.26 e, para o painel, Node 24.

Na raiz de um clone do repositório:

```sh
make release
```

`make release` constrói o painel (`web/dist`) e gera um binário estático (`CGO_ENABLED=0`) por plataforma em `dist/`:

```text
dist/gateway_<versão>_linux_amd64
dist/gateway_<versão>_linux_arm64
dist/gateway_<versão>_darwin_amd64
dist/gateway_<versão>_darwin_arm64
dist/gateway_<versão>_windows_amd64.exe
```

Copie o da sua plataforma para um diretório do `PATH` com o nome `gateway` (`gateway.exe` no Windows). Não há mais nada a instalar.

Sem `make` (no Windows, por exemplo), os mesmos passos à mão, só para a plataforma atual:

```sh
cd web && npm ci && npm run build && cd ..
CGO_ENABLED=0 go build -trimpath -o gateway ./cmd/gateway     # gateway.exe no Windows
```

Sem Node, `go build ./cmd/gateway` também funciona: o binário sai com uma página provisória no lugar do painel, e a API e o proxy funcionam normalmente.

### Docker

Na raiz de um clone do repositório:

```sh
docker build -t gateway .
docker run --rm -p 8080:8080 -p 8081:8081 gateway
```

Assim ele sobe sem rotas; crie-as pelo painel em <http://localhost:8081> ou pela API. Para usar os seus arquivos, monte o diretório que contém `gateway.json` e `routes/` em `/data`:

```sh
docker run --rm -p 8080:8080 -p 8081:8081 -v "$PWD:/data" gateway
```

No Git Bash do Windows, que reescreve caminhos passados a programas nativos, use `MSYS_NO_PATHCONV=1 docker run ... -v "$(pwd -W):/data" gateway`; no PowerShell, `-v "${PWD}:/data"`.

O `Dockerfile` constrói o painel com Node, o binário com Go e entrega uma imagem final mínima (distroless, sem shell) com só o binário, rodando como usuário sem privilégio. Na imagem:

- a configuração é lida de `/data/gateway.json` (`GATEWAY_CONFIG`), e as rotas de `/data/routes`;
- `/data` é um volume. A API e o modo aprendizado gravam nesses arquivos, então o diretório precisa aceitar escrita pelo usuário do contêiner. No Linux, acrescente `--user "$(id -u):$(id -g)"` para gravar como você;
- sem `gateway.json`, o gateway sobe com os padrões e sem rotas, e cria o arquivo na primeira alteração pela API ou pelo painel;
- para falar com serviços que rodam na máquina hospedeira, use `http://host.docker.internal:<porta>` como `upstream` (no Linux, com `--add-host=host.docker.internal:host-gateway`).

`make docker` faz o `docker build` gravando a versão do código na imagem.

## Primeiros passos

Num diretório vazio, crie `gateway.json`:

```json
{
  "schemaVersion": 1,
  "ports": { "traffic": 8080, "admin": 8081 },
  "seed": 42
}
```

e `routes/payments.yaml`, apontando para um serviço seu (aqui, um que atende em `localhost:9001`):

```yaml
schemaVersion: 1
name: payments
upstream: http://localhost:9001
match:
  path: /payments/*
stripPrefix: true
overrides:
  - name: flaky
    match:
      path: /payments/balance
      method: GET
    probability: 0.3
    respond:
      status: 503
      body:
        error: indisponível
```

Suba o gateway no mesmo diretório:

```sh
gateway                        # ou: gateway -config caminho/do/gateway.json
```

e fale com ele:

```sh
curl -i localhost:8080/payments/balance      # 30% das vezes, 503 sintetizado
curl -s localhost:8081/api/status            # estado do processo
curl -s localhost:8081/api/exchanges         # o que passou pelo gateway
```

O painel fica em <http://localhost:8081>.

Uma resposta interceptada traz `X-Gateway: route=payments; override=payments/flaky; intervention=synthesized`; uma encaminhada, só `X-Gateway: route=payments`. Um path que nenhuma rota casa recebe `404` com `{"error": "no_route"}`.

Para ver tudo funcionando sem ter serviços próprios, [`examples/`](examples) tem dois serviços de brinquedo, rotas prontas e dois scripts: `examples/run.sh` sobe o ambiente, e `examples/e2e.sh` o verifica de ponta a ponta.

## Dois formatos: JSON no processo, YAML nas rotas

O critério é um formato por tipo de leitor:

- **`gateway.json`** configura o processo (portas, seed, histórico, aprendizado). É lido por máquina e por scripts de bootstrap, quase nunca editado à mão, e JSON não tem ambiguidade de tipos.
- **`routes/*.yaml`** descreve as rotas, um documento por rota. São editados à mão o tempo todo, e YAML aceita comentários, corpos multilinha e tem menos pontuação. Um arquivo por rota também evita conflito de merge quando duas pessoas criam rotas diferentes.

Os dois declaram `schemaVersion`. O gateway recusa um documento com versão maior que a que ele conhece, em vez de interpretá-lo pela metade.

## `gateway.json`

Todos os campos são opcionais: o que falta vem da variável de ambiente ou do padrão. O arquivo também é opcional; sem ele, o gateway sobe com os padrões e avisa no log.

```json
{
  "schemaVersion": 1,
  "ports": {
    "traffic": 8080,
    "admin": 8081
  },
  "seed": 42,
  "history": {
    "backend": "sqlite",
    "path": "data/history.db",
    "capacity": 1000,
    "record": true,
    "expose": true
  },
  "capture": {
    "maxBodyBytes": 65536
  },
  "learning": {
    "enabled": false
  },
  "routesDir": "routes"
}
```

| Chave | Padrão | Significado |
|---|---|---|
| `ports.traffic` | `8080` | porta do tráfego encaminhado |
| `ports.admin` | `8081` | porta do painel e da API; precisa ser diferente da de tráfego |
| `seed` | ausente | semente dos sorteios; ausente, a aleatoriedade não se repete entre execuções |
| `history.backend` | `memory` | `memory` (anel em memória), `ndjson` (um arquivo, uma troca por linha) ou `sqlite` (arquivo local, driver puro em Go) |
| `history.path` | `gateway-history.ndjson` ou `gateway-history.db` | arquivo do `ndjson` e do `sqlite` |
| `history.capacity` | `1000` | trocas mantidas pelo backend `memory` |
| `history.record` | `true` | registra as trocas |
| `history.expose` | `true` | permite ler o histórico pela API e pelo painel |
| `capture.maxBodyBytes` | `65536` | corpos maiores são truncados na captura (o tráfego não é afetado) |
| `learning.enabled` | `false` | [modo aprendizado](#modo-aprendizado) |
| `routesDir` | `routes` | diretório dos documentos de rota |

Caminhos relativos (`history.path`, `routesDir`) partem do diretório do próprio `gateway.json`, não de onde o processo roda. Se o backend do histórico não inicializa (um arquivo sem permissão de escrita, por exemplo), o gateway recusa subir em vez de cair para memória sem avisar.

## Documentos de rota

Cada arquivo `.yaml` ou `.yml` em `routes/` é uma rota; os demais arquivos são ignorados. Dois documentos com o mesmo `name`, ou com o mesmo host e o mesmo path, impedem a carga, e a mensagem nomeia os dois arquivos. Um erro de validação nomeia arquivo, campo, linha e coluna.

```yaml
# routes/payments.yaml
schemaVersion: 1
name: payments
upstream: http://localhost:9001
match:
  path: /payments/*          # curinga de sufixo; sem *, path exato
  # host: pagamentos.local   # alternativa (ou complemento) ao path
stripPrefix: true            # /payments/balance chega ao upstream como /balance
rewriteHost: false           # true: o upstream recebe o próprio host em vez do Host original
timeout: 5s                  # sem resposta nesse tempo, o cliente recebe 504

overrides:
  # Resposta forçada: sem probability, vale sempre que o critério casa.
  - name: charge-declined
    match:
      path: /payments/charges
      method: POST
      headers:
        X-Scenario: declined
    respond:
      status: 402
      headers:
        Content-Type: application/json
      body:
        error: card_declined

  # Falha em 30% das consultas; as demais seguem para o upstream.
  - name: balance-flaky
    match:
      path: /payments/balance
      method: GET
    probability: 0.3
    respond:
      status: 503
      headers:
        Retry-After: "1"
      body:
        error: balance_unavailable

  # Só latência: sem respond, a resposta vem do upstream, atrasada.
  - name: lookup-slow
    match:
      pathRegex: ^/payments/charges/[^/]+$
      method: GET
    latency:
      min: 200ms
      max: 900ms

  # Derruba a conexão sem resposta em 10% das chamadas, por 10 minutos
  # ou 50 aplicações, o que vier primeiro.
  - name: flaky-network
    match:
      path: /payments/*
    probability: 0.1
    drop: true
    ttl: 10m
    maxApplications: 50

  # Desligado: fica no documento, mas não intervém.
  - name: maintenance
    enabled: false
    match:
      path: /payments/*
    respond:
      status: 503
```

Rota:

| Campo | Significado |
|---|---|
| `name` | nome único da rota |
| `upstream` | URL do serviço. Sem ele, só os overrides respondem, e o resto recebe `501` |
| `match.path` | path exato (`/health`) ou curinga de sufixo (`/api/payments/*`); parâmetros de segmento (`:id`) só no path de um override |
| `match.host` | casa pelo `Host` da requisição; rotas com host têm precedência |
| `stripPrefix` | remove a parte fixa do padrão antes de encaminhar |
| `rewriteHost` | troca o `Host` original pelo do upstream (servidores com virtual host) |
| `timeout` | tempo limite de resposta do upstream (`504` ao estourar); sem conexão, `502` |
| `overrides` | lista de overrides |

Entre as rotas, a mais específica vence: host antes de path, path exato antes de curinga, curinga mais longo antes do mais curto.

Override:

| Campo | Significado |
|---|---|
| `name` | nome único dentro da rota; o override é identificado como `rota/nome` |
| `enabled` | `false` desliga; ausente equivale a ligado |
| `match.path` / `match.pathRegex` | path exato (`/viacep/01001000/json`), com parâmetros de segmento (`/viacep/:id/json`), curinga de sufixo (`/viacep/*`) ou expressão regular (um dos dois campos). Cada `:nome` casa exatamente um segmento não vazio: `/viacep/:id/json` casa com `/viacep/40415345/json`, não com `/viacep/40415345/extra/json`. O nome segue `[A-Za-z_][A-Za-z0-9_]*`, não se repete no mesmo path e não divide o segmento com o curinga. O path é o que o cliente enviou, antes do `stripPrefix` |
| `match.method` | método HTTP |
| `match.headers`, `match.query` | por nome, um texto (igualdade) ou um objeto com um de `equals`, `regex`, `json`, `contains` |
| `match.body` | o mesmo, aplicado ao corpo (`json` compara a estrutura, ignorando formatação e ordem das chaves) |
| `respond.status` | padrão `200` |
| `respond.headers` | cada cabeçalho é um texto ou uma lista (cabeçalho repetido, como vários `Set-Cookie`) |
| `respond.body` | texto, ou estrutura YAML enviada como JSON (com `Content-Type: application/json` inferido) |
| `probability` | fração das requisições selecionadas em que o override vale; padrão `1.0` |
| `latency` | atraso fixo (`2s`) ou sorteado em intervalo (`{min: 200ms, max: 900ms}`) |
| `drop` | encerra a conexão sem resposta |
| `ttl` | tempo de vida a partir do registro do override |
| `maxApplications` | número de aplicações até expirar |
| `source` | escrito pelo gateway nos overrides aprendidos ou derivados de uma troca; aponta a troca de origem |

Quando mais de um override casa, vale o mais específico: path exato; depois path com parâmetros de segmento (entre eles, o de mais segmentos literais); depois expressão regular; depois curinga, do mais longo ao mais curto; e, em empate, o que declara mais critérios. O que ainda empatar é resolvido pela ordem no documento. Overrides desligados ou expirados ficam fora dessa disputa.

## Variáveis de ambiente

A precedência é **ambiente, depois `gateway.json`, depois o padrão**. Um valor vindo do ambiente fica travado: a API e o painel recusam alterá-lo (`409 locked`, nomeando a variável). `GET /api/settings` mostra a origem efetiva de cada valor, para responder "por que está usando memória se eu configurei SQLite?" num comando.

| Variável | Chave em `gateway.json` | Valores |
|---|---|---|
| `GATEWAY_CONFIG` | (nenhuma) | caminho do `gateway.json`; padrão `./gateway.json`. A opção `-config` tem o mesmo efeito |
| `GATEWAY_TRAFFIC_PORT` | `ports.traffic` | inteiro |
| `GATEWAY_ADMIN_PORT` | `ports.admin` | inteiro |
| `GATEWAY_SEED` | `seed` | inteiro não negativo |
| `GATEWAY_HISTORY_BACKEND` | `history.backend` | `memory`, `ndjson` ou `sqlite` |
| `GATEWAY_HISTORY_PATH` | `history.path` | caminho do arquivo (relativo ao diretório de trabalho) |
| `GATEWAY_HISTORY_CAPACITY` | `history.capacity` | inteiro |
| `GATEWAY_HISTORY_RECORD` | `history.record` | `true` ou `false` |
| `GATEWAY_HISTORY_EXPOSE` | `history.expose` | `true` ou `false` |
| `GATEWAY_CAPTURE_MAX_BODY_BYTES` | `capture.maxBodyBytes` | inteiro, em bytes |
| `GATEWAY_LEARNING` | `learning.enabled` | `true` ou `false` |
| `GATEWAY_ROUTES_DIR` | `routesDir` | diretório (relativo ao diretório de trabalho) |

Uma variável vazia é tratada como ausente. O caso típico é o backend do histórico mudar por máquina, enquanto as rotas são as mesmas:

```sh
GATEWAY_HISTORY_BACKEND=sqlite GATEWAY_HISTORY_PATH=ci-history.db gateway
```

## O override: mock e caos são a mesma coisa

Não há um mecanismo de mock e outro de caos. Há o override: um critério de seleção, uma resposta declarada e os modificadores `probability`, `latency`, `drop`, `ttl` e `maxApplications`.

- **Mock** é um override com `probability: 1.0` (ou sem `probability`): toda requisição selecionada recebe a resposta declarada.
- **Caos** é o mesmo override com `probability: 0.3`: 30% recebem a resposta declarada, e os outros 70% seguem para o upstream como se o override não existisse.
- **Só latência**: sem `respond`, o override não intercepta; a resposta vem do upstream, atrasada.
- **Queda**: `drop: true` fecha a conexão sem resposta.

A rota encaminha tudo por padrão; os overrides interceptam só o que selecionam. Na ordem fixa do caminho da requisição, o gateway sorteia primeiro se o override vale, depois a queda e o atraso, e só então responde (sintetizando ou indo ao upstream). O atraso é aplicado com a resposta pronta, antes de escrevê-la: por isso o tempo injetado e o tempo do upstream são medidos separadamente e **se somam**. No histórico, cada troca traz `timing.upstreamMs`, `timing.injectedMs` e `timing.gatewayMs`, que o painel desenha como waterfall.

Com `seed` definido, o sorteio de cada requisição deriva de `(seed, número de sequência de chegada)`: a mesma sequência de requisições produz as mesmas decisões em outra execução.

A API cria overrides também a partir de uma troca capturada (`POST /api/routes/{rota}/overrides/derive`), com a resposta real pré-preenchida; no painel, é o botão de criar override na troca aberta.

## Modo aprendizado

Com `learning.enabled` ligado, cada combinação nova de método e path que passa por uma rota e é respondida pelo upstream vira um override **desligado** no documento da rota, com o status, os cabeçalhos e o corpo observados e um bloco `source` apontando a troca de origem:

```yaml
  - name: get-catalog-products-p3
    enabled: false
    match:
      path: /catalog/products/p3
      method: GET
    respond:
      status: 200
      headers:
        Content-Type: application/json
      body:
        id: p3
        name: Lapiseira 0,5 mm
    source:
      kind: learned
      exchange: 01M2V1XH0K450DW9ZKFEPGG80J
      at: 2026-09-18T20:04:24.72Z
```

Desligado, ele não muda o tráfego. Ele é o ponto de partida para o próximo gesto: ligar, ajustar a resposta ou dar uma probabilidade de falha. O aprendizado grava depois de a resposta ser entregue, fora do caminho da requisição.

O path gravado é generalizado: um segmento que parece identificar um registro — só dígitos, UUID, ou alfanumérico com dígitos e ao menos 8 caracteres — vira parâmetro de segmento (`:id`, `:id2`…), e o resto fica literal. `GET /viacep/40415345/json` e depois `GET /viacep/01001000/json` geram um único override, `get-viacep-id-json`, com path `/viacep/:id/json` e a resposta da primeira troca; `/api/users/me` e `/api/users/42` geram dois, `/api/users/me` e `/api/users/:id`, porque `me` não é identificador. A heurística é conservadora: `json`, `charge` ou `ch_123` ficam literais, e um identificador não reconhecido (um slug, por exemplo) só custa uma regra a mais, que você generaliza trocando o segmento por `:id`.

Uma combinação já é conhecida quando a rota tem override, ligado ou não, do mesmo método com o path generalizado igual ou cujo path (exato ou com parâmetros) casa com a requisição; curingas e expressões regulares não contam, para que os endpoints sob eles também sejam aprendidos. Ao gravar um override generalizado, os aprendidos de path exato que ele cobre — ainda desligados e sem outros critérios — são substituídos por ele, no mesmo lugar do documento. Um aprendido que você ligou ou restringiu fica, e continua valendo antes do generalizado.

Ligue e desligue sem reiniciar:

```sh
curl -s -X PUT localhost:8081/api/learning -H 'Content-Type: application/json' -d '{"enabled":true}'
```

O modo vale para todas as rotas. Veja os [limites](#limites-assumidos) sobre paths com identificadores e sobre comentários.

## Reconfiguração a quente

Nada exige reiniciar o processo:

- **Rotas e overrides**: toda escrita pela API ou pelo painel valida, grava o documento e aplica. Depois de editar os arquivos à mão, `curl -s -X POST localhost:8081/api/reload` relê tudo. Um documento inválido é recusado, e a configuração anterior continua em vigor.
- **Processo, inclusive portas e backend do histórico**: `PATCH /api/settings` altera o `gateway.json` e aplica:

  ```sh
  curl -s -X PATCH localhost:8081/api/settings \
    -H 'Content-Type: application/merge-patch+json' -d '{"ports":{"traffic":9080}}'
  ```

  A porta nova é aberta antes de a antiga fechar. Se não abrir, nada muda (`409 port_unavailable`). A porta antiga para de aceitar conexões e termina as requisições em curso. A troca do backend do histórico segue o mesmo protocolo.
- Requisições em curso terminam com a configuração em que começaram; a troca da configuração é atômica.

## Paridade entre arquivo, API e painel

Tudo o que se configura nos arquivos também se configura pela API, e o painel é só um cliente dessa API. O painel mostra, ao lado dos controles, o documento YAML ou JSON correspondente, atualizado ao vivo e editável.

| No arquivo | Na API |
|---|---|
| criar `routes/x.yaml` | `POST /api/routes` ou `PUT /api/routes/x/document` |
| editar um campo da rota | `PATCH /api/routes/x` |
| editar o YAML à mão | `PUT /api/routes/x/document` |
| apagar `routes/x.yaml` | `DELETE /api/routes/x` |
| acrescentar, editar ou remover um override | `POST`, `PATCH` e `DELETE /api/routes/x/overrides[/y]` |
| `enabled: false` num override | `PATCH /api/routes/x/overrides/y` com `{"enabled": false}` |
| editar `gateway.json` | `PATCH /api/settings` ou `PUT /api/settings/document` |
| `learning.enabled` | `PUT /api/learning` |
| editar os arquivos fora do painel | `POST /api/reload` |

Escrever pela API reescreve **só** o documento da rota tocada, de forma atômica; os outros ficam byte a byte iguais. A referência completa, com um exemplo de `curl` por operação, está em [`docs/api.md`](docs/api.md).

## Limites assumidos

Decisões conscientes, com o custo à vista:

- **Comentários se perdem na escrita e no aprendizado.** Quando a API, o painel ou o modo aprendizado grava um documento de rota, ele é reserializado: comentários e a ordem original das chaves daquele documento se perdem. O dano fica contido à rota tocada, e o painel avisa antes da primeira escrita num documento com comentários. Se os comentários importam, mantenha o original versionado e trabalhe numa cópia (é o que `examples/run.sh` faz).
- **O determinismo é por ordem de chegada.** O seed fixa as decisões por número de sequência, não por conteúdo. Reproduzir uma execução exige reenviar as requisições na mesma ordem; requisições concorrentes podem chegar em ordem diferente de uma execução para outra, então a reprodutibilidade estrita pede envio serial.
- **A queda de conexão é degradada em HTTP/2.** Em HTTP/1.1 o gateway fecha o socket sem resposta. Em HTTP/2 não há socket próprio da requisição, e a queda vira o cancelamento abrupto do stream. A troca capturada registra qual dos dois aconteceu em `dropMode` (`hijack`, `stream_reset`, ou `abort` quando a conexão HTTP/1.x não permite sequestro).
- **Identificadores que a heurística não reconhece geram um override por valor.** O aprendizado generaliza dígitos, UUIDs e códigos alfanuméricos com dígitos, mas um slug como `/posts/meu-titulo` ou um código curto como `ch_123` vira um override por valor. Troque o segmento por `:id` num deles e remova os demais; daí em diante os valores que ele cobre são conhecidos e não geram regra nova. `GET /api/learning` conta os aprendidos por rota.
- **Trocar o backend do histórico não migra as trocas.** As trocas anteriores continuam no backend antigo, intactas (o arquivo NDJSON ou SQLite continua no disco); o novo começa vazio. A resposta da API e o painel avisam no momento da troca.

Também ficam de fora, por ora: TLS na porta de tráfego (o gateway fala HTTP com o cliente e HTTP ou HTTPS com o upstream), autenticação na porta de administração (o uso previsto é local) e retenção automática nos backends persistentes.

## Desenvolvimento

```sh
make test          # go test ./...
make race          # testes com -race (exige CGO e um compilador C)
make lint          # gofmt e go vet
make build         # bin/gateway com o web/dist atual
make web           # constrói o painel em web/dist
make web-clean     # volta web/dist ao placeholder versionado
make release       # painel + binários por plataforma em dist/
make docker        # imagem Docker
```

- Código Go em `cmd/gateway` e `internal/`; o painel em `web/` (Vite, React e TypeScript; ver [`web/README.md`](web/README.md)).
- O `web/dist/index.html` versionado é um placeholder, para que `go build` funcione num clone sem Node. Depois de `make web`, não faça commit dele; `make web-clean` o restaura.
- O planejamento está em `openspec/changes/add-test-gateway/`.
