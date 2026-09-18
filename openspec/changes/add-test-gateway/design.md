## Context

Projeto greenfield: o repositório contém apenas o scaffold do OpenSpec. O ambiente já dispõe de Go 1.26, Node 24 e Docker. Ver `proposal.md` — Why para a motivação e os `specs/` desta mudança para os requisitos.

Três restrições moldam todas as decisões abaixo:

- **Artefato único.** O produto precisa ser um binário estático sem dependência de runtime. Isso condiciona a escolha do driver de banco e empurra o frontend para dentro do executável.
- **Ambiente de desenvolvimento, não produção.** O volume é de dezenas de requisições por segundo, não dezenas de milhares. Clareza e previsibilidade valem mais que throughput.
- **A interface é a última etapa.** Cada capability precisa ser exercitável pela API de administração antes de existir tela, sob pena de o projeto virar refém do frontend.

## Goals / Non-Goals

**Goals:**

- Um único conceito de intervenção, em vez de mock e caos como mecanismos paralelos.
- Caminho de requisição com etapas explícitas e ordem fixa, para que o waterfall da captura seja uma leitura direta do que o gateway fez, e não uma reconstrução aproximada.
- Determinismo real sob concorrência quando um seed é declarado.
- Troca de configuração sem lock no caminho quente e sem derrubar requisições em curso.
- Configuração que não produza conflito de merge quando duas pessoas criam rotas diferentes.
- API de administração completa o bastante para operar o gateway inteiro por `curl`, com a interface como cliente dessa mesma API.

**Non-Goals:**

- Otimizar throughput ou alocação. Correção e legibilidade vêm primeiro.
- Terminar TLS na porta de tráfego nesta mudança (ver Open Questions).
- Autenticação na porta de administração nesta mudança (ver Open Questions).

## Decisions

### Override único no lugar de mock e caos

Não existem dois mecanismos. Existe o override: um seletor, uma resposta declarada, e os modificadores `probability`, `latency`, `drop` e `ttl`. Probabilidade `1.0` é resposta forçada; `0.3` é injeção de falha; o que não é sorteado segue para o upstream.

*Por quê:* as ferramentas de referência separam mock de caos, e a separação vaza para o usuário — no MockServer você configura uma expectation numa tela e um chaos profile em outra, para controlar a mesma rota. A distinção é interna, não conceitual: forçar uma resposta é injetar caos com probabilidade máxima. Unificar corta metade da superfície de configuração, metade da UI e a pergunta "isso eu configuro como mock ou como caos?".

*Consequência que vale registrar:* um override com `latency` e sem `respond` não intercepta nada — apenas atrasa a resposta do upstream. Isso cai naturalmente do modelo e cobre o caso "quero só deixar esse endpoint lento" sem inventar um terceiro conceito.

*Alternativa descartada:* manter os dois com nomes distintos, espelhando MockServer e Smocker. Rejeitada porque preserva uma sobreposição que o usuário enxergou antes de o código existir.

### Proxy sobre `net/http/httputil.ReverseProxy`

Usar o `ReverseProxy` da biblioteca padrão com a API `Rewrite`, em vez de escrever o encaminhamento do zero ou adotar `fasthttp`.

*Por quê:* streaming, `Flush` incremental, trailers, HTTP/2 e `Expect: 100-continue` já vêm resolvidos e testados. Escrever isso à mão é onde proxies caseiros erram. O `fasthttp` seria mais rápido, mas não fala HTTP/2, tem API divergente da stdlib e a velocidade não é o gargalo aqui.

### Ordem fixa e única do caminho de requisição

Toda requisição na porta de tráfego percorre exatamente esta sequência:

1. Resolver a rota (host, depois padrão de path mais específico).
2. Abrir o registro de captura e iniciar a cronometragem.
3. Resolver o override aplicável: entre os que selecionam a requisição, o mais específico.
4. Se há override, sortear em ordem fixa — aplicação, queda, atraso — de uma única fonte de aleatoriedade.
5. Se a aplicação não foi sorteada: seguir para o upstream como se o override não existisse.
6. Se houve queda: encerrar a conexão e fechar o registro.
7. Se o override declara `respond`: sintetizar a resposta. Caso contrário, encaminhar ao upstream.
8. Aplicar o atraso sorteado **depois** de a resposta estar pronta e antes de escrevê-la ao cliente.
9. Fechar o registro com os tempos decompostos.

*Por quê o atraso no passo 8 e não antes do upstream:* atrasar depois mantém o tempo real do upstream e o tempo injetado como grandezas independentes e diretamente mensuráveis. Atrasar antes obrigaria a subtrair um valor do outro para exibir o waterfall, e a subtração erra sempre que o upstream oscila. O custo é que a latência total passa a ser a soma, e não a máxima — que é justamente o comportamento esperado por quem pede "essa rota demora 2s a mais".

*Por quê sortear tudo no passo 4:* o sorteio precisa ser independente do que acontece depois. Se a aplicação fosse decidida só quando o upstream responde, o mesmo seed produziria sequências diferentes conforme a disponibilidade do upstream, e o determinismo iria embora.

### Determinismo por número de sequência, não por conteúdo

Cada requisição recebe um número de sequência monotônico ao entrar. Quando há seed configurado, a fonte de aleatoriedade daquela requisição é derivada de `(seed, sequência)`, com `math/rand/v2` e um gerador PCG. As decisões de uma requisição não dependem de nenhuma outra.

*Por quê:* um único gerador compartilhado entre goroutines produziria resultados dependentes da ordem de escalonamento — determinismo aparente, que quebra sob carga. Derivar por sequência dá reprodutibilidade genuína com zero contenção no sorteio.

*Alternativa descartada:* derivar do hash da requisição. Seria determinístico por conteúdo, mas então a mesma requisição repetida seria interceptada sempre ou nunca — o que destrói o próprio sentido de "aplica em 30% das vezes".

*Limite assumido e documentado:* o determinismo é sobre a **ordem de chegada**. Reproduzir uma execução exige reenviar as requisições na mesma ordem.

### Configuração em documentos por rota

`gateway.json` guarda a configuração do processo. Cada rota vive em seu próprio documento YAML sob `routes/`, e a carga funde todos num snapshot.

*Por quê:* é o padrão do `nginx conf.d` e dos manifests do Kubernetes, e resolve três problemas de uma vez. Duas pessoas criando rotas diferentes não tocam no mesmo arquivo, então não há conflito de merge. O diff do PR passa a dizer "adicionou a rota de pagamentos" em vez de mostrar uma mancha no meio de um arquivo grande. E a escrita pela API fica cirúrgica: mexer numa rota reescreve só aquele documento.

*Trade-off explícito:* a reserialização **não preserva comentários nem a ordem original das chaves** do documento tocado. Preservá-los exigiria manipular a árvore sintática do YAML e manter essa manipulação correta a cada evolução do schema — custo desproporcional. A decisão é assumir a perda, que agora fica contida a um documento de rota em vez do arquivo inteiro, documentá-la no README e avisar na interface antes da primeira escrita destrutiva.

### JSON no processo, YAML nas rotas

Os dois formatos convivem de propósito.

*Por quê:* `gateway.json` é lido por máquina e por scripts de bootstrap, quase nunca editado à mão, e JSON evita ambiguidade de tipos. Os documentos de rota são editados à mão o tempo todo, e YAML sobrevive melhor a isso — comentários, blocos de corpo multilinha e menos ruído de pontuação. Forçar um formato só otimizaria a consistência da documentação em detrimento de quem usa.

### Precedência ambiente sobre arquivo sobre padrão

Variáveis de ambiente vencem `gateway.json`, que vence os padrões embutidos. A origem efetiva de cada valor é consultável pela API.

*Por quê:* o backend de armazenamento muda por ambiente — memória na máquina do desenvolvedor, SQLite no CI — enquanto as rotas são as mesmas. Ambiente é o lugar certo para o que varia por máquina; arquivo, para o que varia por projeto. A consulta de origem existe porque a pergunta "por que está usando memória se eu configurei SQLite?" precisa ter resposta em um comando, não em uma sessão de depuração.

### Resolução por lista ordenada, em dois níveis

Rotas e overrides são ordenados na carga por especificidade e resolvidos por varredura linear — primeiro a rota, depois o override dentro dela.

*Por quê:* uma árvore de prefixos seria mais rápida e bem mais difícil de auditar quando o usuário perguntar por que determinada requisição caiu em determinado override. Com a dezena de rotas típica de um ambiente de desenvolvimento, a varredura é irrelevante no perfil, e a ordem de precedência fica legível no próprio código.

### Configuração como snapshot imutável trocado atomicamente

O snapshot fundido vive num `atomic.Pointer` para uma estrutura imutável, com os índices de roteamento já pré-computados. A recarga valida, constrói um snapshot novo e troca o ponteiro. Requisições em curso seguem com o snapshot que capturaram na entrada.

*Por quê:* elimina locks no caminho quente e resolve de graça o requisito de que uma recarga não perturbe requisições em andamento. Validar antes de trocar é o que garante que uma configuração inválida preserve a anterior — a troca só acontece depois que o snapshot novo existe inteiro.

### Armazenamento do histórico atrás de uma interface, com três implementações

O histórico é acessado por uma interface única, com implementações em memória (anel, padrão), arquivo NDJSON e SQLite local, escolhidas por variável de ambiente. A mesma bateria de testes de contrato roda contra as três.

*Por quê:* o histórico em memória basta para a máquina do desenvolvedor, mas não para investigar o que aconteceu num CI que já terminou, nem para correlacionar duas execuções. NDJSON serve a quem quer processar com `jq` sem subir banco. SQLite serve à consulta com filtro sobre volume grande, que é exatamente onde o arquivo plano degrada.

*Restrição que decide o driver:* o driver comum de SQLite em Go usa CGO, o que quebraria a compilação cruzada e o binário estático. A implementação MUST usar um driver puro em Go (`modernc.org/sqlite`). É mais lento, e nesse uso isso não importa.

*Por que a falha de inicialização não cai para memória:* subir silenciosamente com outro backend produziria a pior forma de erro — tudo funcionando, nada sendo persistido, descoberto horas depois. Melhor recusar iniciar.

### Tempo real por SSE, não WebSocket

A interface recebe as trocas novas por *Server-Sent Events*, com envio agregado a no máximo uma atualização por segundo.

*Por quê:* o fluxo é unidirecional — a interface apenas consome, e toda escrita já passa pela API REST. O `EventSource` reconecta sozinho, o que atende ao requisito de reconexão automática sem código de retry próprio. WebSocket traria bidirecionalidade que não seria usada e um caminho de reconexão para manter à mão.

### Queda de conexão limitada a HTTP/1.1

A queda usa `http.Hijacker` para fechar o socket sem escrever resposta. Isso não existe em HTTP/2.

*Decisão:* em HTTP/2 a queda é degradada para o cancelamento abrupto do stream, e a captura registra qual dos dois comportamentos ocorreu. A alternativa — recusar configurar queda quando a porta serve HTTP/2 — foi descartada por tornar o comportamento dependente de um detalhe de transporte que o usuário não escolheu.

### Frontend embutido via `go:embed`

O frontend é React + TypeScript construído com Vite para `web/dist`, embutido com `//go:embed all:web/dist`. Um `index.html` mínimo é versionado nesse diretório para que `go build` funcione em um clone limpo, sem exigir Node. Durante o desenvolvimento do frontend, o servidor do Vite encaminha as chamadas de API para a porta de administração.

*Por quê o placeholder versionado:* sem ele, `go build` quebra em qualquer máquina que não tenha rodado o build do frontend antes — incluindo CI e a máquina de quem só quer mexer no backend.

### Duas portas, sem paths reservados

Tráfego e administração ficam em portas distintas (padrão `8080` e `8081`), e o processo recusa iniciar se forem iguais.

*Por quê:* qualquer path reservado na porta de tráfego seria um prefixo que o usuário não poderia usar nas próprias rotas. É a mesma separação que o Smocker adota, e pelo mesmo motivo.

## Risks / Trade-offs

- **Latência injetada soma em vez de sobrepor** → Assumido conscientemente para manter o waterfall exato. Documentar no campo de latência da interface que o valor é acrescido ao tempo real do upstream.
- **Determinismo depende da ordem de chegada** → Documentar junto à configuração do seed. Requisições concorrentes podem receber números de sequência em ordem distinta entre execuções; a reprodutibilidade estrita exige envio serial.
- **Queda de conexão se comporta de forma diferente em HTTP/2** → Registrar na captura qual comportamento ocorreu, para que o desenvolvedor nunca precise adivinhar.
- **Comentários do documento de rota se perdem ao escrever pela API** → Escrita atômica por documento, aviso no README e alerta na interface antes da primeira escrita destrutiva. O dano fica contido a uma rota.
- **Três backends de armazenamento triplicam a superfície de teste** → Uma única bateria de testes de contrato roda contra as três implementações; nenhuma ganha teste próprio salvo para o que é específico dela.
- **Driver SQLite puro em Go é mais lento que o baseado em CGO** → Irrelevante no volume alvo, e é o preço de manter o binário estático e a compilação cruzada.
- **Dois formatos de configuração podem confundir** → O critério é simples e vai no README: processo em JSON, rotas em YAML. Um formato por tipo de leitor.
- **A interface é a última etapa e pode ficar sem fôlego** → Mitigado pela decisão de a API cobrir todas as operações: mesmo sem nenhuma tela, o produto é utilizável por `curl` e por documento de rota.
- **`ReverseProxy` reescreve cabeçalhos por conta própria** → Cobrir com testes os cenários de cabeçalho da spec de roteamento, especialmente o acúmulo de `X-Forwarded-For` e a preservação opcional do `Host`.

## Migration Plan

Não há migração: o projeto não tem código anterior nem consumidores.

Para a distribuição, tanto `gateway.json` quanto cada documento de rota carregam um campo de versão de schema desde a primeira versão. O gateway recusa carregar um documento cuja versão de schema seja maior que a que ele conhece, com mensagem explícita — evita que uma configuração futura seja interpretada pela metade por um binário antigo.

Entrega: binários por plataforma e imagem Docker, ambos produzidos pelo mesmo build que embute o frontend.

## Open Questions

- **Terminação TLS na porta de tráfego.** Hoje o gateway fala HTTP em claro com o cliente e pode falar HTTPS com o upstream. Aceitar HTTPS do cliente exigiria gestão de certificado e provavelmente uma autoridade local. Não altera nenhuma spec desta mudança e pode ser acrescentado depois como configuração da porta.
- **Autenticação na porta de administração.** O uso alvo é local. Se o gateway passar a ser compartilhado por um time numa rede interna, será preciso decidir entre token estático e proxy autenticador à frente. Nenhuma das duas opções muda a API nem as specs atuais.
- **Retenção no backend persistente.** Em memória o anel resolve. Em NDJSON e SQLite, o histórico cresce sem limite até alguém apagar. Decidir depois entre rotação por tamanho, por idade ou nenhuma — não altera o contrato de leitura já especificado.
