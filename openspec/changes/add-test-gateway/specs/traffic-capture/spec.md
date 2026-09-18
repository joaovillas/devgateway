## Purpose

Registra cada troca HTTP que atravessa o gateway com o tempo decomposto e o armazena no backend escolhido pelo ambiente, para que o desenvolvedor veja o caminho percorrido pela chamada, distinga a lentidão real do upstream daquela que o gateway injetou e consiga ler as trocas uma a uma.

## ADDED Requirements

### Requirement: Registro das trocas HTTP

O gateway SHALL registrar cada troca que atravessa a porta de tráfego contendo, no mínimo: identificador único, instante de início, método, path, query, cabeçalhos e corpo da requisição, rota casada, override aplicado quando houver, upstream de destino, status, cabeçalhos e corpo da resposta, e tamanhos em bytes. O identificador MUST ser estável e suficiente para recuperar a troca isoladamente. Corpos maiores que o limite configurado MUST ser truncados e marcados como truncados.

#### Scenario: Troca encaminhada é registrada por completo

- **WHEN** uma requisição é encaminhada a um upstream e respondida
- **THEN** o histórico passa a conter uma troca com identificador próprio, a rota casada, o upstream de destino, o status e os dados de requisição e resposta

#### Scenario: Corpo acima do limite é truncado

- **WHEN** uma resposta traz um corpo maior que o limite de captura configurado
- **THEN** a troca registra o corpo truncado, sinaliza que houve truncamento e preserva o tamanho real em bytes

#### Scenario: Troca sem rota casada também é registrada

- **WHEN** chega uma requisição que nenhuma rota atende e o gateway responde `404`
- **THEN** o histórico registra a troca sem rota casada e sem upstream

### Requirement: Decomposição da latência

Cada troca registrada SHALL informar separadamente o tempo total, o tempo consumido pelo upstream, o tempo de atraso injetado por override e o tempo restante atribuído ao próprio gateway. Quando nenhum atraso é injetado, o tempo injetado MUST ser zero.

#### Scenario: Tempo injetado separado do tempo real

- **WHEN** um override com latência de `2s` deixa a requisição seguir para um upstream que responde em `150ms`
- **THEN** a troca registra aproximadamente `2s` de tempo injetado e aproximadamente `150ms` de tempo de upstream, e o total reflete a soma acrescida do overhead do gateway

#### Scenario: Sem override o tempo injetado é zero

- **WHEN** uma requisição é encaminhada sem que nenhum override a intercepte
- **THEN** a troca registra tempo injetado igual a zero

#### Scenario: Resposta sintetizada não contabiliza tempo de upstream

- **WHEN** uma requisição é respondida por um override, sem contato com upstream
- **THEN** a troca registra tempo de upstream igual a zero

### Requirement: Distinção entre resposta do upstream e intervenção do gateway

O registro de cada troca SHALL indicar se o resultado veio do upstream ou foi produzido por um override, nomeando o override responsável quando houver. Quedas de conexão MUST ser registradas, ainda que nenhuma resposta tenha sido enviada.

#### Scenario: Resposta sintetizada é marcada

- **WHEN** um override sintetiza um `503`
- **THEN** a troca registra status `503`, marca-o como sintetizado pelo gateway e nomeia o override responsável

#### Scenario: Erro do upstream não é marcado como intervenção

- **WHEN** o upstream responde `500` sem que nenhum override tenha interceptado
- **THEN** a troca registra status `500` sem marcação de intervenção

#### Scenario: Queda de conexão é registrada

- **WHEN** um override derruba a conexão sem enviar resposta
- **THEN** a troca é registrada como encerrada por queda, sem status de resposta

### Requirement: Consulta e filtragem do histórico

O gateway SHALL permitir consultar o histórico em ordem cronológica inversa, com paginação, e filtrá-lo por rota, upstream, override, método, path, faixa de status, presença de intervenção e janela de tempo. Filtros combinados MUST ser aplicados de forma conjuntiva.

#### Scenario: Filtro por rota

- **WHEN** o histórico é consultado filtrando pela rota `payments`
- **THEN** somente trocas casadas por essa rota são retornadas

#### Scenario: Filtro por faixa de status

- **WHEN** o histórico é consultado filtrando por status entre `500` e `599`
- **THEN** somente trocas com status nessa faixa são retornadas

#### Scenario: Filtro por intervenção

- **WHEN** o histórico é consultado filtrando por trocas com intervenção do gateway
- **THEN** somente trocas sintetizadas ou atrasadas por override são retornadas

#### Scenario: Ordem e paginação

- **WHEN** o histórico contém 150 trocas e a primeira página de 50 é solicitada
- **THEN** são retornadas as 50 trocas mais recentes, da mais nova para a mais antiga, com um indicador de continuação

### Requirement: Leitura individual e navegação por cursor

O gateway SHALL permitir recuperar uma única troca pelo seu identificador, com o conteúdo completo ainda que a listagem o resuma. O gateway SHALL também permitir navegar o histórico item a item a partir de uma troca, obtendo a anterior e a seguinte sem carregar a listagem inteira. A navegação MUST respeitar os filtros ativos quando informados.

#### Scenario: Troca recuperada pelo identificador

- **WHEN** uma troca é solicitada pelo seu identificador
- **THEN** o gateway devolve a troca completa, com requisição, resposta e tempos decompostos

#### Scenario: Identificador inexistente

- **WHEN** é solicitada uma troca cujo identificador não existe no histórico
- **THEN** o gateway responde que a troca não foi encontrada

#### Scenario: Navegação item a item

- **WHEN** é solicitada a troca seguinte a partir de um identificador
- **THEN** o gateway devolve a troca imediatamente posterior na ordem do histórico, ou informa que não há mais itens

#### Scenario: Navegação respeita o filtro ativo

- **WHEN** a navegação item a item é feita com um filtro por faixa de status
- **THEN** as trocas percorridas são apenas as que satisfazem o filtro

### Requirement: Armazenamento plugável do histórico

O gateway SHALL armazenar o histórico no backend selecionado pela configuração — variável de ambiente, `gateway.json` ou API de administração —, entre memória, arquivo NDJSON e SQLite local. Sem seleção explícita, o gateway MUST usar memória. O comportamento observável de registro, consulta e navegação MUST ser o mesmo em todos os backends. Quando o backend selecionado não pode ser inicializado, o gateway MUST recusar iniciar com uma mensagem que identifique o backend e a causa, em vez de silenciosamente cair para outro.

#### Scenario: Memória é o padrão

- **WHEN** o gateway inicia sem nenhuma configuração selecionando o backend
- **THEN** o histórico é mantido em memória, com capacidade limitada e descarte das trocas mais antigas ao atingi-la

#### Scenario: Histórico sobrevive ao reinício em backend persistente

- **WHEN** o backend NDJSON ou SQLite está selecionado, trocas são registradas e o processo é reiniciado
- **THEN** as trocas anteriores continuam disponíveis para consulta e navegação

#### Scenario: Mesmo comportamento entre backends

- **WHEN** a mesma sequência de requisições atravessa o gateway em cada um dos backends
- **THEN** listagem, filtros, leitura por identificador e navegação por cursor produzem os mesmos resultados

#### Scenario: Backend indisponível impede a inicialização

- **WHEN** o backend SQLite é selecionado com um caminho em que o gateway não consegue escrever
- **THEN** o gateway recusa iniciar, informando o backend e a causa da falha

#### Scenario: Capacidade excedida em memória

- **WHEN** o backend é memória, a capacidade é de 100 trocas e chega a centésima primeira
- **THEN** a troca mais antiga deixa de constar no histórico e a nova é registrada

### Requirement: Exposição do histórico configurável

O gateway SHALL permitir ligar e desligar a exposição do histórico por configuração. Com a exposição desligada, os endpoints de listagem, leitura e navegação MUST responder que o recurso está desabilitado, e a interface MUST indicar isso em vez de apresentar uma lista vazia. O registro das trocas MUST poder ser desligado de forma independente da exposição.

#### Scenario: Exposição desligada

- **WHEN** a exposição do histórico está desligada e a listagem é solicitada
- **THEN** o gateway responde que o recurso está desabilitado, sem devolver trocas

#### Scenario: Registro desligado

- **WHEN** o registro está desligado e chegam requisições
- **THEN** nenhuma troca é registrada e o encaminhamento segue funcionando normalmente

#### Scenario: Limpeza sob demanda

- **WHEN** a limpeza do histórico é solicitada
- **THEN** o histórico fica vazio no backend em uso e as trocas seguintes voltam a ser registradas normalmente
