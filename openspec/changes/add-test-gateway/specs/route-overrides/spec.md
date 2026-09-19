## Purpose

Intercepta paths específicos dentro de uma rota que, por padrão, apenas encaminha para o upstream, permitindo forçar uma resposta, atrasá-la, derrubá-la ou fazer tudo isso apenas em uma fração das chamadas — um único mecanismo no lugar da distinção entre mock e injeção de caos.

## ADDED Requirements

### Requirement: Interceptação seletiva com passthrough por padrão

Uma rota sem nenhum override SHALL encaminhar todas as requisições ao seu upstream. Um override MUST interceptar somente as requisições que satisfazem seus critérios; as demais MUST seguir para o upstream sem qualquer alteração de comportamento.

#### Scenario: Rota sem override encaminha tudo

- **WHEN** uma rota com curinga `/api/payments/*` e nenhum override recebe requisições para três paths distintos
- **THEN** as três são encaminhadas ao upstream e nenhuma resposta é sintetizada

#### Scenario: Override intercepta apenas o que casa

- **WHEN** existe um override para `/api/payments/bilulu` e chegam requisições para `/api/payments/bilulu` e para `/api/payments/charge`
- **THEN** a primeira é respondida pelo override sem contatar o upstream e a segunda é encaminhada normalmente

#### Scenario: Rota sem upstream e sem override casado

- **WHEN** uma rota não declara upstream e chega uma requisição que nenhum override intercepta
- **THEN** o gateway responde `501` com um corpo que informa a rota atingida e a ausência de upstream e de override correspondente

### Requirement: Critérios de seleção do override

Um override SHALL selecionar requisições por path e MAY restringir adicionalmente por método, cabeçalhos, parâmetros de query e corpo. O path MUST aceitar forma exata, curinga de sufixo, parâmetros de segmento (`/viacep/:id/json`, em que cada `:nome` casa exatamente um segmento não vazio) e expressão regular. Os demais critérios MUST aceitar os operadores de igualdade exata, expressão regular, igualdade JSON e conteúdo de substring. Quando um override declara vários critérios, todos MUST casar.

#### Scenario: Path exato

- **WHEN** um override declara o path `/api/payments/bilulu` e chega uma requisição para esse path
- **THEN** o override casa

#### Scenario: Path com parâmetro de segmento

- **WHEN** um override declara o path `/viacep/:id/json` e chegam requisições para `/viacep/40415345/json` e para `/viacep/40415345/extra/json`
- **THEN** o override casa com a primeira e não casa com a segunda

#### Scenario: Path com curinga

- **WHEN** um override declara o path `/api/payments/*` e chega uma requisição para `/api/payments/charge/42`
- **THEN** o override casa

#### Scenario: Restrição por método

- **WHEN** um override declara o path `/api/payments/charge` restrito ao método `POST` e chega um `GET` para esse path
- **THEN** o override não casa e a requisição é encaminhada ao upstream

#### Scenario: Igualdade JSON no corpo ignora a ordem das chaves

- **WHEN** um override exige o corpo igual ao JSON `{"a":1,"b":2}` e chega uma requisição com corpo `{"b":2,"a":1}`
- **THEN** o override casa

#### Scenario: Um critério que não casa impede a interceptação

- **WHEN** um override exige o cabeçalho `X-Tenant: acme` e chega uma requisição sem esse cabeçalho
- **THEN** o override não casa

### Requirement: Precedência por especificidade

Quando mais de um override casa com a mesma requisição, o gateway SHALL aplicar o mais específico. Um path exato MUST prevalecer sobre um path com parâmetros de segmento, que MUST prevalecer sobre um curinga (entre paths com parâmetros, prevalece o de mais segmentos literais), um curinga mais longo MUST prevalecer sobre um mais curto, e entre paths de igual especificidade MUST prevalecer o override com mais critérios declarados. Empates remanescentes MUST ser resolvidos pela ordem de declaração no documento da rota.

#### Scenario: Path exato vence o curinga

- **WHEN** existem overrides para `/api/payments/*` e para `/api/payments/bilulu`, e chega uma requisição para `/api/payments/bilulu`
- **THEN** o override de path exato é aplicado

#### Scenario: Parâmetro de segmento entre exato e curinga

- **WHEN** existem overrides para `/viacep/*`, `/viacep/:id/json` e `/viacep/01001000/json`, e chegam requisições para `/viacep/01001000/json` e `/viacep/40415345/json`
- **THEN** a primeira é atendida pelo override exato e a segunda pelo de parâmetro de segmento

#### Scenario: Curinga mais longo vence o mais curto

- **WHEN** existem overrides para `/api/*` e `/api/payments/*` e chega uma requisição para `/api/payments/charge`
- **THEN** o override de `/api/payments/*` é aplicado

#### Scenario: Mais critérios vence entre paths equivalentes

- **WHEN** dois overrides declaram o mesmo path e um deles também exige o método `POST`, e chega um `POST` para esse path
- **THEN** o override que exige o método é aplicado

### Requirement: Resposta declarada pelo override

Um override que intercepta SHALL responder com o status, os cabeçalhos e o corpo que declara. Quando o status não é informado, o gateway MUST usar `200`. O corpo MAY ser declarado como texto ou como estrutura JSON, e neste caso o gateway MUST serializá-lo e definir o tipo de conteúdo correspondente, salvo se o override declarar outro.

#### Scenario: Resposta completa devolvida

- **WHEN** um override declara status `200`, o cabeçalho `X-Source: override` e um corpo JSON, e intercepta uma requisição
- **THEN** o cliente recebe exatamente esse status, esse cabeçalho e esse corpo, e o upstream não é contatado

#### Scenario: Status padrão

- **WHEN** um override declara apenas um corpo e intercepta uma requisição
- **THEN** o cliente recebe status `200`

#### Scenario: Tipo de conteúdo inferido do corpo JSON

- **WHEN** um override declara o corpo como estrutura JSON e não declara tipo de conteúdo
- **THEN** a resposta é serializada como JSON e carrega o tipo de conteúdo correspondente

### Requirement: Frequência de cada efeito

Cada efeito declarado por um override — a resposta declarada, a latência e a queda de conexão — MAY declarar sua própria frequência, entre `0.0` e `1.0`, e o gateway SHALL sortear cada efeito de forma independente a cada requisição que o override seleciona. Um efeito sem frequência declarada MUST valer sempre. Um efeito não sorteado MUST ser ignorado como se não estivesse declarado, e uma requisição em que nenhum efeito foi sorteado MUST seguir para o upstream sem alteração. Valores fora do intervalo MUST ser recusados na validação. Para compatibilidade, uma frequência declarada para o override inteiro MUST valer como padrão de todos os seus efeitos que não declaram a própria.

#### Scenario: Efeito sem frequência vale sempre

- **WHEN** um override declara uma resposta sem frequência e intercepta 20 requisições
- **THEN** as 20 são respondidas por ele

#### Scenario: Cada efeito tem a sua frequência

- **WHEN** um override declara resposta `503` com frequência `0.3` e latência de `1s` sem frequência, com seed fixo, e chegam 1000 requisições que ele seleciona
- **THEN** a quantidade respondida com `503` fica dentro da tolerância estatística esperada para 30%, as demais são encaminhadas ao upstream, e todas as 1000 são atrasadas

#### Scenario: Queda com frequência própria

- **WHEN** um override declara queda com frequência `0.05` e resposta declarada sem frequência, e chegam 1000 requisições que ele seleciona
- **THEN** cerca de 5% das requisições são derrubadas e as demais recebem a resposta declarada

#### Scenario: Frequência zero nunca aplica

- **WHEN** um efeito declara frequência `0.0` e chegam requisições que o override seleciona
- **THEN** esse efeito nunca é aplicado, e os demais efeitos do override seguem valendo

#### Scenario: Frequência fora do intervalo é recusada

- **WHEN** um documento de rota declara frequência `1.5` num efeito
- **THEN** a configuração é recusada com uma mensagem que aponta o campo inválido

#### Scenario: Frequência do override vale para os efeitos sem a sua

- **WHEN** um override declara frequência `0.3` para si, uma resposta sem frequência própria e uma latência com frequência `1.0`
- **THEN** a resposta é sorteada em 30% das requisições e a latência é aplicada em todas

### Requirement: Latência e queda de conexão

Um override MAY declarar latência, como valor fixo ou como intervalo com mínimo e máximo sorteado uniformemente a cada requisição, e MAY declarar queda de conexão. A latência MUST ser aplicada depois de a resposta estar pronta e antes de entregá-la ao cliente. A queda MUST encerrar a conexão sem enviar resposta alguma e MUST ter precedência sobre a resposta declarada.

#### Scenario: Latência fixa

- **WHEN** um override declara latência de `2s` e intercepta uma requisição
- **THEN** a resposta chega ao cliente ao menos `2s` após a requisição

#### Scenario: Latência sorteada no intervalo

- **WHEN** um override declara latência com mínimo `100ms` e máximo `500ms` e intercepta 50 requisições
- **THEN** todos os atrasos observados ficam entre `100ms` e `500ms` e não são todos iguais

#### Scenario: Intervalo invertido é recusado

- **WHEN** um documento de rota declara latência com mínimo `500ms` e máximo `100ms`
- **THEN** a configuração é recusada com uma mensagem que aponta o campo inválido

#### Scenario: Queda encerra sem resposta

- **WHEN** um override declara queda de conexão e intercepta uma requisição
- **THEN** o cliente observa a conexão encerrada sem receber status nem corpo

#### Scenario: Latência aplicada a uma rota sem interceptação

- **WHEN** um override declara apenas latência, sem resposta declarada, e seleciona uma requisição
- **THEN** a requisição é encaminhada ao upstream normalmente e a resposta dele é entregue após o atraso

### Requirement: Expiração por tempo e por contagem

Um override MAY declarar um tempo de vida e MAY declarar um número máximo de aplicações. O gateway SHALL deixar de aplicá-lo quando qualquer um dos dois se esgota, sem intervenção do usuário, e MUST expor o tempo restante e a contagem de aplicações enquanto ele está ativo. Um override sem nenhum dos dois MUST permanecer ativo até ser removido.

#### Scenario: Override expira pelo tempo

- **WHEN** um override com tempo de vida de `30s` é registrado e se passam mais de `30s`
- **THEN** as requisições seguintes voltam a ser encaminhadas ao upstream, sem nenhuma ação do usuário

#### Scenario: Override expira pela contagem

- **WHEN** um override declara no máximo duas aplicações e chegam três requisições que ele seleciona
- **THEN** as duas primeiras são respondidas pelo override e a terceira é encaminhada ao upstream

#### Scenario: Tempo restante e contagem consultáveis

- **WHEN** um override com tempo de vida de `60s` e limite de cinco aplicações está ativo há `20s` e já aplicou duas vezes
- **THEN** a consulta informa aproximadamente `40s` restantes e duas aplicações realizadas de um limite de cinco

#### Scenario: Override sem limites permanece

- **WHEN** um override sem tempo de vida e sem limite de contagem é registrado e se passa um período prolongado
- **THEN** ele continua sendo aplicado até ser removido explicitamente

### Requirement: Determinismo por seed

O gateway SHALL aceitar um seed para as decisões probabilísticas. Com o mesmo seed e a mesma sequência de requisições, o gateway MUST tomar exatamente as mesmas decisões de aplicação, efeito a efeito, na mesma ordem. Sem seed declarado, o gateway MUST usar uma origem de aleatoriedade não reproduzível.

#### Scenario: Mesmo seed reproduz a mesma sequência

- **WHEN** o gateway é executado duas vezes com o mesmo seed e recebe em cada execução a mesma sequência de 100 requisições
- **THEN** o conjunto de requisições interceptadas é idêntico nas duas execuções

#### Scenario: Seeds distintos divergem

- **WHEN** o gateway é executado com dois seeds diferentes sobre a mesma sequência de requisições
- **THEN** o conjunto de requisições interceptadas difere entre as execuções

### Requirement: Identificação da intervenção

Toda resposta sintetizada ou atrasada por um override SHALL identificar, no cabeçalho `X-Gateway` definido pela spec `gateway-routing`, o override responsável e o tipo de intervenção aplicada. A troca correspondente MUST ser registrada como interceptada.

#### Scenario: Resposta interceptada é identificável

- **WHEN** um override sintetiza uma resposta `503`
- **THEN** o cabeçalho `X-Gateway` da resposta indica o override responsável e que a resposta foi sintetizada pelo gateway

#### Scenario: Resposta do upstream não é marcada

- **WHEN** o upstream responde `500` por conta própria e nenhum override interceptou a requisição
- **THEN** o cabeçalho `X-Gateway` da resposta identifica apenas a rota, sem override nem intervenção

### Requirement: Override ligado e desligado

Um override SHALL poder ser declarado desligado. Um override desligado MUST NOT participar da seleção nem da precedência, e as requisições que ele selecionaria MUST seguir como se ele não existisse. A ausência do campo MUST equivaler a ligado. Desligar e religar um override MUST preservar todos os seus demais campos.

#### Scenario: Override desligado não intercepta

- **WHEN** um override que sintetiza `503` está desligado e chega uma requisição que ele seleciona
- **THEN** a requisição é encaminhada ao upstream

#### Scenario: Desligado não esconde o menos específico

- **WHEN** um override de path exato está desligado e um override de curinga que também casa está ligado
- **THEN** o override de curinga é aplicado

#### Scenario: Religar restaura o comportamento

- **WHEN** o override desligado é religado sem outra alteração
- **THEN** as requisições seguintes voltam a ser respondidas por ele com a mesma resposta declarada

### Requirement: Aprendizado de endpoints

O gateway SHALL oferecer um modo aprendizado, desligado por padrão e alterável em tempo de execução. Com o modo desligado, o gateway MUST apenas aplicar a configuração existente, sem gravar nada além do histórico. Com o modo ligado, cada combinação de método e path ainda não conhecida numa rota, cuja requisição foi encaminhada e respondida pelo upstream, MUST ser gravada no documento dessa rota como um override desligado, com critério de método e de path generalizado — segmentos que identificam um registro (só dígitos, UUID, ou hexadecimal/alfanumérico com dígitos e ao menos 8 caracteres) substituídos por parâmetros de segmento `:id`, `:id2`… e os demais mantidos literais — e resposta declarada pré-preenchida com o status, todos os cabeçalhos e o corpo observados — excluídos apenas `Date`, `Content-Length` e cabeçalhos hop-by-hop. O override gravado MUST registrar a troca de origem e se o corpo foi truncado na captura. Uma combinação é conhecida quando a rota já possui override, ligado ou desligado, do mesmo método com path exato ou com parâmetros de segmento que casa com a requisição; overrides de curinga ou de expressão regular não tornam uma combinação conhecida. Ao gravar um override generalizado, os overrides aprendidos anteriormente com path exato do mesmo método cujo path generaliza para o dele MUST ser substituídos por ele, sem duplicar a regra, desde que continuem como foram aprendidos (desligados e sem critérios adicionais); um aprendido que o usuário ligou ou restringiu é preservado.

#### Scenario: Endpoints novos são aprendidos

- **WHEN** o modo aprendizado está ligado e chegam `GET /api/teste` e depois `GET /api/teste2` por uma rota com upstream
- **THEN** o documento da rota passa a conter dois overrides desligados, um para cada path, cada um com a resposta real que o upstream devolveu

#### Scenario: Endpoint aprendido não intercepta

- **WHEN** um endpoint foi aprendido e a mesma requisição chega de novo
- **THEN** ela é encaminhada ao upstream normalmente, porque o override aprendido está desligado

#### Scenario: Endpoint conhecido não é duplicado

- **WHEN** o modo aprendizado está ligado e `GET /api/teste` chega pela segunda vez
- **THEN** o documento da rota continua com um único override para `GET /api/teste`

#### Scenario: Modo desligado não grava

- **WHEN** o modo aprendizado está desligado e chegam requisições para paths novos
- **THEN** nenhum documento de rota é modificado e as trocas constam apenas no histórico

#### Scenario: Somente respostas do upstream são aprendidas

- **WHEN** o modo aprendizado está ligado e a requisição é respondida por override, por `404` sem rota ou por `502` do gateway
- **THEN** nenhum override é aprendido a partir dela

#### Scenario: Identificador vira parâmetro

- **WHEN** o modo aprendizado está ligado e chegam `GET /viacep/40415345/json` e depois `GET /viacep/01001000/json`
- **THEN** o documento da rota passa a conter um único override desligado com path `/viacep/:id/json`

#### Scenario: Segmento literal preservado

- **WHEN** o modo aprendizado está ligado e chegam `GET /api/users/me` e `GET /api/users/42`
- **THEN** são aprendidos `/api/users/me` e `/api/users/:id` como overrides distintos

#### Scenario: Aprendido exato é absorvido

- **WHEN** a rota já tem um override aprendido com path `/viacep/40415345/json` e o aprendizado grava `/viacep/:id/json` para o mesmo método
- **THEN** o override exato aprendido é substituído pelo generalizado e o documento passa a ter um só

#### Scenario: Corpo truncado sinalizado

- **WHEN** o modo aprendizado está ligado e a resposta observada excede o limite de captura
- **THEN** o override aprendido registra que o corpo está incompleto

### Requirement: Derivação de override a partir de troca capturada

O gateway SHALL permitir criar um override a partir de uma troca já registrada no histórico, preenchendo os critérios com os dados da requisição observada e a resposta declarada com o que o upstream devolveu. O override derivado MUST poder ser revisado antes de passar a valer.

#### Scenario: Override derivado reproduz a troca observada

- **WHEN** um override é derivado de uma troca capturada e uma requisição equivalente chega em seguida
- **THEN** o gateway responde com o mesmo status, cabeçalhos e corpo que o upstream havia devolvido naquela troca

#### Scenario: Troca inexistente

- **WHEN** é solicitada a derivação a partir de um identificador de troca que não existe no histórico
- **THEN** a operação é recusada com um erro que informa que a troca não foi encontrada

#### Scenario: Derivação a partir de troca truncada

- **WHEN** é solicitada a derivação a partir de uma troca cujo corpo foi truncado na captura
- **THEN** a operação é recusada ou o override é criado sinalizando explicitamente que o corpo está incompleto
