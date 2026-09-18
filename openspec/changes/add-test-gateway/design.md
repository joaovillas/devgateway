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
3. Resolver o override aplicável: entre os ligados que selecionam a requisição, o mais específico.
4. Se há override, sortear em ordem fixa — aplicação, queda, atraso — de uma única fonte de aleatoriedade.
5. Se a aplicação não foi sorteada: seguir para o upstream como se o override não existisse.
6. Se houve queda: encerrar a conexão e fechar o registro.
7. Se o override declara `respond`: sintetizar a resposta. Caso contrário, encaminhar ao upstream.
8. Aplicar o atraso sorteado **depois** de a resposta estar pronta e antes de escrevê-la ao cliente — seja ela a do upstream, a sintetizada ou uma resposta de erro do próprio gateway (`501`, `502`, `504`). Se o cliente desiste durante o atraso, nada lhe foi entregue: a troca fica sem status, com a desistência (`client_canceled`) anotada.
9. Fechar o registro com os tempos decompostos e, com o modo aprendizado ligado, aprender o endpoint fora do caminho da requisição.

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

*Ordem do histórico pela chegada, não pela conclusão:* a troca só é gravada quando termina, mas o histórico a posiciona pela chegada — instante de início, depois número de sequência, e a ordem de gravação apenas como desempate. Assim uma requisição lenta que chegou antes não aparece como mais nova que as rápidas que chegaram depois dela, e a listagem, a paginação e a navegação item a item concordam com os instantes exibidos. O cursor de paginação carrega essa chave e a época do histórico, que avança a cada limpeza.

*Por que a falha de inicialização não cai para memória:* subir silenciosamente com outro backend produziria a pior forma de erro — tudo funcionando, nada sendo persistido, descoberto horas depois. Melhor recusar iniciar.

### Tempo real por SSE, não WebSocket

A interface recebe as trocas novas por *Server-Sent Events*, com envio agregado a no máximo uma atualização por segundo.

*Por quê:* o fluxo é unidirecional — a interface apenas consome, e toda escrita já passa pela API REST. O `EventSource` reconecta sozinho, o que atende ao requisito de reconexão automática sem código de retry próprio. WebSocket traria bidirecionalidade que não seria usada e um caminho de reconexão para manter à mão.

### Queda de conexão limitada a HTTP/1.1

A queda usa `http.Hijacker` para fechar o socket sem escrever resposta. Isso não existe em HTTP/2.

*Decisão:* em HTTP/2 a queda é degradada para o cancelamento abrupto do stream, e a captura registra qual dos comportamentos ocorreu em `dropMode`: `hijack` (HTTP/1.1, socket fechado) ou `stream_reset` (HTTP/2). Um terceiro valor, `abort`, cobre o caso raro de uma conexão HTTP/1.x cujo `ResponseWriter` não permite sequestro (um writer intermediário sem `Unwrap`, por exemplo): o handler é abortado com `http.ErrAbortHandler` e o servidor fecha a conexão sem resposta. Registrá-lo como `hijack` ou `stream_reset` esconderia do desenvolvedor o que de fato aconteceu. A alternativa — recusar configurar queda quando a porta serve HTTP/2 — foi descartada por tornar o comportamento dependente de um detalhe de transporte que o usuário não escolheu.

### Frontend embutido via `go:embed`

O frontend é React + TypeScript construído com Vite para `web/dist`, embutido com `//go:embed all:web/dist`. Um `index.html` mínimo é versionado nesse diretório para que `go build` funcione em um clone limpo, sem exigir Node. Durante o desenvolvimento do frontend, o servidor do Vite encaminha as chamadas de API para a porta de administração.

*Por quê o placeholder versionado:* sem ele, `go build` quebra em qualquer máquina que não tenha rodado o build do frontend antes — incluindo CI e a máquina de quem só quer mexer no backend.

### Duas portas, sem paths reservados, trocadas a quente

Tráfego e administração ficam em portas distintas (padrão `8080` e `8081`), e o processo recusa iniciar se forem iguais.

*Por quê:* qualquer path reservado na porta de tráfego seria um prefixo que o usuário não poderia usar nas próprias rotas. É a mesma separação que o Smocker adota, e pelo mesmo motivo.

*Troca a quente:* os listeners ficam num supervisor, fora do snapshot. Mudar uma porta abre o listener novo primeiro; só se ele abrir o servidor antigo recebe `Shutdown`, que para de aceitar conexões e deixa as requisições em curso terminarem. Se o listener novo falhar, nada muda e o erro é reportado. A troca do backend do histórico segue o mesmo protocolo: inicializa o novo, troca o ponteiro, fecha o antigo. O histórico não é migrado entre backends — migrar exigiria reescrever volumes arbitrários no caminho da requisição de configuração, e a pergunta "onde estão minhas trocas antigas?" tem resposta simples: no backend anterior, intacto.

### Transparência: repassar tudo, acrescentar um cabeçalho

O gateway repassa método, path, query, corpo, todos os cabeçalhos e o `Host` como chegaram, e só acrescenta os `X-Forwarded-*` (preservando valores já presentes) e um único cabeçalho próprio, `X-Gateway`, na ida e na volta. Os cabeçalhos hop-by-hop são a única remoção, porque o HTTP proíbe repassá-los; o `Upgrade` de WebSocket e os trailers continuam funcionando pelo `ReverseProxy`.

*Por quê:* o gateway existe para testar o cliente contra o serviço real; qualquer alteração silenciosa invalida o teste. O `Host` original por padrão segue a mesma lógica; a rota declara `rewriteHost` quando o upstream exige o próprio host, como servidores com virtual host.

*Um cabeçalho só:* `X-Gateway` carrega a rota e, quando há intervenção, o override e o tipo (`route=payments; override=payments/flaky; intervention=synthesized`). Quando o override sintetiza e também atrasa, as duas intervenções aparecem, separadas por vírgula e na ordem em que acontecem (`intervention=synthesized,delayed`), como na lista `interventions` da troca capturada. Vários cabeçalhos `X-Gateway-*` seriam mais fáceis de ler um a um, mas multiplicariam o que o gateway injeta no tráfego.

### Aprendizado grava overrides desligados

Com o modo aprendizado ligado, cada método e path novo respondido pelo upstream vira um override `enabled: false` no documento da rota, com a resposta real pré-preenchida e um bloco `source` registrando a troca de origem.

*Por quê:* o endpoint aprendido já é o ponto de partida do gesto que o usuário quer fazer em seguida — ligar caos ou customizar a resposta. Guardá-lo como uma lista paralela de "endpoints conhecidos" criaria um segundo conceito ao lado do override, exatamente a separação que este projeto recusa. Desligado por padrão, ele não altera o tráfego até o usuário decidir.

*Como:* o aprendizado roda depois de a resposta ser entregue, fora do caminho da requisição, e grava pelo mesmo mecanismo da API — escrita atômica do documento sob o mutex de escrita, seguida de reconstrução do snapshot. Uma combinação é conhecida quando já há override com o mesmo path exato e método; curingas não contam, para que endpoints sob um curinga também sejam aprendidos.

*Fidelidade da resposta aprendida:* um cabeçalho repetido (vários `Set-Cookie`, por exemplo) é gravado como lista de valores em `respond.headers` — cada cabeçalho aceita um texto ou uma lista — e a resposta sintetizada o repete, um por valor. O corpo JSON só vira estrutura quando todos os números cabem sem perda (inteiros até `uint64`, decimais que o `float64` reproduz); senão fica como texto, idêntico ao observado. Um path que contém `*` literal (ou que não começa com `/`) não pode ser escrito como path exato, porque o `*` seria curinga: o critério gravado é `pathRegex` ancorado (`^` + path escapado + `$`), que também conta como path exato na verificação de endpoint conhecido. Um override montado que ainda assim não passe na validação não é gravado, e o endpoint deixa de ser candidato até o processo reiniciar, com um único aviso no log.

*Troca de origem sem histórico:* com `history.record` desligado a troca é observada pelo aprendizado mas não vai para o histórico; o override aprendido sai então sem `source.exchange`, para não apontar para uma troca inexistente, e mantém `source.kind`, `source.at` e `source.bodyIncomplete`. Um override derivado (que sempre parte de uma troca do histórico) continua exigindo `source.exchange`.

## Risks / Trade-offs

- **Latência injetada soma em vez de sobrepor** → Assumido conscientemente para manter o waterfall exato. Documentar no campo de latência da interface que o valor é acrescido ao tempo real do upstream.
- **Tempo de upstream inclui a espera por um cliente lento em streaming** → O corpo da resposta é copiado ao cliente sem ser acumulado; quando o cliente lê mais devagar do que o upstream envia, a cópia espera por ele e essa espera entra no tempo de upstream. Separá-las exigiria acumular a resposta inteira, o que quebraria o streaming. Assumido e documentado no modelo da troca (`exchange.Timing`).
- **Determinismo depende da ordem de chegada** → Documentar junto à configuração do seed. Requisições concorrentes podem receber números de sequência em ordem distinta entre execuções; a reprodutibilidade estrita exige envio serial.
- **Queda de conexão se comporta de forma diferente em HTTP/2** → Registrar na captura qual comportamento ocorreu, para que o desenvolvedor nunca precise adivinhar.
- **Comentários do documento de rota se perdem ao escrever pela API** → Escrita atômica por documento, aviso no README e alerta na interface antes da primeira escrita destrutiva. O dano fica contido a uma rota.
- **Aprendizado de paths com identificadores gera um override por identificador** (`/users/1`, `/users/2`...) → Assumido: o usuário consolida num override de curinga e remove os aprendidos. Documentar no README e mostrar na interface a contagem de aprendidos por rota.
- **Aprendizado reescreve o documento da rota** → Herda a perda de comentários da escrita pela API; o aviso da interface e do README vale também aqui.
- **Troca de backend não migra o histórico** → Registrado na resposta da API e na interface no momento da troca.
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
- **Consolidação automática no aprendizado.** Detectar segmentos variáveis (`/users/123` → `/users/*`) reduziria o ruído, mas erra em paths legítimos parecidos. Fica para depois, sem alterar o formato gravado.
- **Autenticação na porta de administração.** O uso alvo é local. Se o gateway passar a ser compartilhado por um time numa rede interna, será preciso decidir entre token estático e proxy autenticador à frente. Nenhuma das duas opções muda a API nem as specs atuais.
- **Retenção no backend persistente.** Em memória o anel resolve. Em NDJSON e SQLite, o histórico cresce sem limite até alguém apagar. Decidir depois entre rotação por tamanho, por idade ou nenhuma — não altera o contrato de leitura já especificado.
