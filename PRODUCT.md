# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Frontend em Vite + React + TypeScript em `web/`, construído para `web/dist` e embutido no binário Go via `go:embed` (decidido em `openspec/changes/add-test-gateway/design.md`). Sem dependência de rede em runtime: fontes e recursos estáticos vão dentro do binário.

## Users

Um desenvolvedor sozinho, na própria máquina. O painel fica aberto numa segunda tela ou aba ao lado do editor e do cliente que ele está construindo. Ele alterna entre escrever código, disparar requisições e olhar o que o gateway fez com elas. Não há um operador dedicado, e o painel é uma ferramenta de apoio, consultada em relances curtos durante o trabalho.

## Product Purpose

Um gateway HTTP self-hosted para desenvolvimento, distribuído como binário Go único. Põe N serviços atrás de uma porta, e com isso dá para provocar sob demanda as condições que quebram o sistema em produção: resposta forçada, erro intermitente, latência e conexão derrubada. Assim o código de retry, timeout e circuit breaker é exercitado antes de ir para produção. O sucesso é quando o desenvolvedor consegue provocar e observar uma falha sem sair do fluxo de trabalho.

## Positioning

Um único conceito, o override, substitui a separação entre mock e caos. Forçar uma resposta é injetar caos com probabilidade `1.0`. O MockServer espalha esse mesmo motor por 21 telas sobre a JVM, e o Smocker não tem taxa de erro percentual. Nenhum dos dois mostra a topologia `cliente → rota → upstream`: ambos entregam só uma lista de log. O waterfall separa o tempo real do upstream do tempo injetado pelo gateway.

## Operating Context

- Duas portas: tráfego (padrão `8080`) e administração (padrão `8081`). O painel vive só na de administração.
- A fonte de verdade são os arquivos versionados no repositório do time: `gateway.json` para o processo e um documento YAML por rota em `routes/`.
- A API de administração cobre todas as operações, e o painel é um cliente dessa mesma API. Tudo o que o painel faz também se faz por `curl`.
- As trocas novas chegam por SSE, agregadas a no máximo uma atualização por segundo.

## Capabilities and Constraints

- **Paridade com os arquivos (requisito do usuário).** Tudo o que se configura editando `gateway.json` ou os YAML de rota MUST ser configurável também pelo painel, e vice-versa. Os dois caminhos são equivalentes.
- Rotas: nome, upstream, casamento por host e/ou path (exato ou curinga de sufixo), remoção de prefixo, preservação do Host e timeout.
- Overrides: seleção por path (exato, curinga ou regex), método, cabeçalhos, query e corpo (operadores equals, regex, json e contains). Também declaram resposta (status, cabeçalhos e corpo), probabilidade, latência (fixa ou em intervalo), queda de conexão, TTL e limite de aplicações.
- Precedência por especificidade entre rotas e entre overrides. Seed determinístico por ordem de chegada.
- Histórico com backend memória, NDJSON ou SQLite. Exposição e registro podem ser desligados de forma independente. Há consulta com filtros, leitura por id e navegação item a item.
- Precedência de configuração: ambiente > `gateway.json` > padrão. A origem de cada valor é consultável.
- Limites assumidos: a escrita pela API reescreve o documento da rota e perde comentários e ordem de chaves. A mudança de porta exige reinício. Em HTTP/2, a queda de conexão vira cancelamento do stream.
- **Em aberto:** como o painel edita valores de `gateway.json` que vêm do ambiente (o ambiente vence o arquivo) e valores que só valem após reinício, como as portas. A spec `control-panel` ainda não cobre a edição do processo.

## Brand Commitments

Sem nome de produto ainda. O repositório chama o produto de "gateway", que é o nome provisório. Não há logo, voz nem identidade definidos. Não inventar marca.

## Evidence on Hand

Não há usuários, depoimentos, métricas nem capturas reais. Os exemplos de configuração estão em `internal/config/testdata/`. As rotas e o tráfego de demonstração virão do ambiente de exemplo (tarefa 9.3). Nada de números de desempenho, clientes ou comparativos inventados.

## Product Principles

1. **Um conceito, um gesto.** Mock e caos são o mesmo override. A interface nunca os separa em lugares diferentes.
2. **Arquivo e tela são equivalentes.** O que se faz num se faz no outro, e o painel deixa claro o que vai para qual documento.
3. **Mostrar o caminho, não só o log.** A topologia e a decomposição do tempo explicam o que aconteceu. Uma lista sozinha não basta.
4. **Não tirar o desenvolvedor do fluxo.** Leitura em relance, ajuste imediato sem etapa de salvar e nada que exija sincronizar ou recarregar.
5. **Nunca esconder por que algo é assim.** Origem de cada valor, override responsável, histórico desabilitado e desconexão aparecem sempre explícitos.

## Accessibility & Inclusion

Nenhum requisito específico do produto foi estabelecido além do piso de qualidade geral: navegação por teclado, contraste e controles contínuos operáveis sem mouse.
