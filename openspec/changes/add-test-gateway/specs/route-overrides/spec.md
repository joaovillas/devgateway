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

Um override SHALL selecionar requisições por path e MAY restringir adicionalmente por método, cabeçalhos, parâmetros de query e corpo. O path MUST aceitar forma exata, curinga de sufixo e expressão regular. Os demais critérios MUST aceitar os operadores de igualdade exata, expressão regular, igualdade JSON e conteúdo de substring. Quando um override declara vários critérios, todos MUST casar.

#### Scenario: Path exato

- **WHEN** um override declara o path `/api/payments/bilulu` e chega uma requisição para esse path
- **THEN** o override casa

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

Quando mais de um override casa com a mesma requisição, o gateway SHALL aplicar o mais específico. Um path exato MUST prevalecer sobre um curinga, um curinga mais longo MUST prevalecer sobre um mais curto, e entre paths de igual especificidade MUST prevalecer o override com mais critérios declarados. Empates remanescentes MUST ser resolvidos pela ordem de declaração no documento da rota.

#### Scenario: Path exato vence o curinga

- **WHEN** existem overrides para `/api/payments/*` e para `/api/payments/bilulu`, e chega uma requisição para `/api/payments/bilulu`
- **THEN** o override de path exato é aplicado

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

### Requirement: Probabilidade de aplicação

Um override MAY declarar uma probabilidade entre `0.0` e `1.0`. Quando declarada, o gateway SHALL aplicar o override apenas nessa fração das requisições que ele seleciona; nas demais a requisição MUST seguir para o upstream como se o override não existisse. A ausência do campo MUST equivaler a `1.0`. Valores fora do intervalo MUST ser recusados na validação da configuração.

#### Scenario: Probabilidade ausente aplica sempre

- **WHEN** um override sem o campo de probabilidade intercepta 20 requisições
- **THEN** as 20 são respondidas pelo override

#### Scenario: Probabilidade fracionária divide entre override e upstream

- **WHEN** um override declara probabilidade `0.3` com seed fixo e chegam 1000 requisições que ele seleciona
- **THEN** a quantidade respondida pelo override fica dentro da tolerância estatística esperada para 30% e as demais são encaminhadas ao upstream

#### Scenario: Probabilidade zero nunca aplica

- **WHEN** um override declara probabilidade `0.0` e chegam requisições que ele seleciona
- **THEN** todas são encaminhadas ao upstream

#### Scenario: Probabilidade fora do intervalo é recusada

- **WHEN** um documento de rota declara probabilidade `1.5` num override
- **THEN** a configuração é recusada com uma mensagem que aponta o campo inválido

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

O gateway SHALL aceitar um seed para as decisões probabilísticas. Com o mesmo seed e a mesma sequência de requisições, o gateway MUST tomar exatamente as mesmas decisões de aplicação de override. Sem seed declarado, o gateway MUST usar uma origem de aleatoriedade não reproduzível.

#### Scenario: Mesmo seed reproduz a mesma sequência

- **WHEN** o gateway é executado duas vezes com o mesmo seed e recebe em cada execução a mesma sequência de 100 requisições
- **THEN** o conjunto de requisições interceptadas é idêntico nas duas execuções

#### Scenario: Seeds distintos divergem

- **WHEN** o gateway é executado com dois seeds diferentes sobre a mesma sequência de requisições
- **THEN** o conjunto de requisições interceptadas difere entre as execuções

### Requirement: Identificação da intervenção

Toda resposta sintetizada ou atrasada por um override SHALL carregar um cabeçalho que identifica o override responsável e o tipo de intervenção aplicada. A troca correspondente MUST ser registrada como interceptada.

#### Scenario: Resposta interceptada é identificável

- **WHEN** um override sintetiza uma resposta `503`
- **THEN** a resposta carrega um cabeçalho indicando o override responsável e que a resposta foi sintetizada pelo gateway

#### Scenario: Resposta do upstream não é marcada

- **WHEN** o upstream responde `500` por conta própria e nenhum override interceptou a requisição
- **THEN** a resposta não carrega o cabeçalho de intervenção

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
