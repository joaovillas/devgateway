## Why

Em ambientes de desenvolvimento, cada serviço sobe numa porta diferente e o cliente precisa conhecer todas elas. Pior: não há como reproduzir sob demanda as condições que quebram o sistema em produção — upstream lento, erro intermitente, conexão derrubada — então código de retry, timeout e circuit breaker é escrito sem nunca ser exercitado.

As ferramentas existentes resolvem isso pela metade. O MockServer tem o motor de caos completo (probabilidade de erro, TTL, waterfall de latência) mas espalha a função por 21 telas e roda sobre JVM. O Smocker é leve e se declara um API gateway, porém não tem taxa de erro percentual — só scripts Lua escritos à mão, mock a mock. Nenhum dos dois mostra a topologia da chamada: ambos entregam lista de log, não o caminho `cliente → rota → upstream`.

E as duas separam "mock" de "caos" como mecanismos distintos, quando na prática são o mesmo gesto com probabilidade diferente: forçar uma resposta é injetar caos com probabilidade `1.0`.

## What Changes

- Novo produto: um gateway HTTP self-hosted para ambientes de desenvolvimento, distribuído como **binário Go único** sem dependências externas.
- **Roteamento reverso** de N serviços upstream atrás de uma porta única, por prefixo de path (inclusive curinga) ou por host.
- **Passthrough por padrão, override cirúrgico por cima.** A rota encaminha tudo; `overrides` interceptam paths específicos. Um único conceito substitui a separação entre mock e caos: o override declara `respond`, e os campos `probability`, `latency`, `drop` e `ttl` determinam se ele vale sempre, às vezes ou por tempo limitado. Probabilidade `1.0` é resposta forçada; `0.3` é caos; o que não é sorteado segue para o upstream.
- **Precedência por especificidade**: override mais específico vence o menos específico, que vence o curinga da rota.
- **Seed determinístico** para tornar o comportamento probabilístico reproduzível entre execuções.
- **Configuração em documentos separados**, com chaves em inglês: `gateway.json` para a configuração do processo e um documento YAML por rota sob `routes/`, fundidos num snapshot na carga. Escrever pela API reescreve apenas o documento da rota afetada.
- **Captura de tráfego** com waterfall que separa o tempo real do upstream do tempo injetado pelo gateway.
- **Armazenamento de log plugável**, escolhido por variável de ambiente: memória (padrão), arquivo NDJSON ou SQLite local.
- **Exposição do log configurável**, com leitura individual por identificador e navegação por cursor, além da listagem paginada.
- **Interface web** embutida no binário (`go:embed`), com mapa de topologia navegável e os controles do override diretamente sobre a rota.

Explicitamente fora de escopo nesta mudança: SLO, contract testing, load testing, gRPC, AsyncAPI, mocking de LLM, breakpoints ao vivo e clustering. São os eixos que inflaram o MockServer e não servem ao problema acima.

Não há quebra de compatibilidade: o projeto não tem código nem consumidores.

## Capabilities

### New Capabilities

- `gateway-routing`: recepção de requisições numa porta única e encaminhamento para o upstream correto por curinga de path ou host, incluindo enriquecimento de cabeçalhos e tratamento de falha do upstream.
- `route-overrides`: interceptação seletiva de paths dentro de uma rota, com critérios de seleção, resposta declarada, probabilidade de aplicação, latência, queda de conexão, expiração por tempo e por contagem, determinismo por seed e derivação a partir de uma troca capturada.
- `traffic-capture`: registro das trocas HTTP que passam pelo gateway, com decomposição do tempo em latência real versus latência injetada, armazenamento plugável e consulta por listagem, identificador ou cursor.
- `gateway-config`: `gateway.json` e documentos de rota em `routes/` como fonte de verdade, com validação na carga, fusão em snapshot, recarga sem reinício e API de administração que lê e grava esses documentos.
- `control-panel`: interface web servida pelo próprio binário, com mapa de topologia navegável, inspeção do tráfego capturado e edição de rotas e overrides.

### Modified Capabilities

Nenhuma. O projeto ainda não possui specs.

## Impact

- **Código**: projeto greenfield. Cria o módulo Go (`cmd/`, `internal/`) e o frontend React + TypeScript embutido via `go:embed`.
- **Dependências**: Go 1.26 e Node para construir o frontend em tempo de build. Em runtime, nenhuma: o driver de SQLite MUST ser uma implementação pura em Go, sem CGO, para preservar o binário estático.
- **Configuração do ambiente**: variáveis de ambiente selecionam o backend de armazenamento do log e seus parâmetros, e ligam ou desligam a exposição do log.
- **Distribuição**: binário por plataforma e imagem Docker.
- **Superfície externa**: duas portas — a do proxy (tráfego) e a de administração (API + UI), para que o painel nunca colida com as rotas encaminhadas.
- **Ordem de construção**: o núcleo do proxy vem primeiro e a interface por último, de modo que cada capability seja verificável por API antes de existir tela.
