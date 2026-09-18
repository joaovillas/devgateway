## Purpose

Dá ao desenvolvedor uma visão navegável do caminho que a chamada percorre e controles diretos sobre os overrides, para que configurar "essa rota falha 30% das vezes" seja um gesto na tela e não a edição de um documento de configuração.

## ADDED Requirements

### Requirement: Interface servida pelo próprio binário

O gateway SHALL servir a interface web a partir do próprio executável, na porta de administração, sem exigir processo adicional, servidor externo ou acesso à rede pública.

#### Scenario: Interface disponível sem dependências

- **WHEN** o binário é executado num ambiente sem acesso à internet e a porta de administração é aberta no navegador
- **THEN** a interface carrega por completo, com todos os seus recursos estáticos

#### Scenario: Interface reflete o estado atual

- **WHEN** a interface é aberta com rotas e overrides já configurados
- **THEN** ela exibe essas rotas e overrides sem exigir nenhuma ação de sincronização

### Requirement: Mapa de topologia

A interface SHALL apresentar a configuração como um mapa navegável que relaciona cliente, rota e upstream. Rotas com override ativo MUST ser visualmente distinguíveis das demais, e selecionar um elemento do mapa MUST filtrar a inspeção de tráfego para aquele elemento.

#### Scenario: Topologia exibida como grafo

- **WHEN** existem três rotas apontando para dois upstreams
- **THEN** o mapa exibe as três rotas e os dois upstreams com as ligações entre eles

#### Scenario: Rota com override ativo é destacada

- **WHEN** uma rota possui override ativo
- **THEN** ela é exibida com destaque visual que a diferencia das demais, indicando o tipo de intervenção e a probabilidade aplicada

#### Scenario: Seleção filtra o tráfego

- **WHEN** uma rota é selecionada no mapa
- **THEN** a inspeção de tráfego passa a exibir somente as trocas dessa rota

#### Scenario: Upstream indisponível é sinalizado

- **WHEN** um upstream vem recusando conexões nas requisições recentes
- **THEN** o mapa sinaliza esse upstream como indisponível

### Requirement: Controle direto do override na rota

A interface SHALL permitir ajustar probabilidade, latência e queda de conexão diretamente sobre o override, por controle contínuo, aplicando a alteração imediatamente, sem etapa separada de confirmação ou salvamento. Quando o override possui tempo de vida ou limite de aplicações, a interface MUST exibir o restante de cada um.

#### Scenario: Ajuste aplicado imediatamente

- **WHEN** a probabilidade de um override é ajustada para 30% no controle contínuo
- **THEN** as requisições seguintes já seguem a nova proporção, sem nenhuma ação adicional de salvamento

#### Scenario: Restante de tempo e de aplicações exibido

- **WHEN** um override com tempo de vida de `60s` e limite de cinco aplicações está ativo
- **THEN** a interface exibe o tempo restante decrescendo e a contagem de aplicações, e remove o destaque da rota quando qualquer um se esgota

#### Scenario: Desligar o override em um gesto

- **WHEN** um override é desligado pela interface
- **THEN** as requisições seguintes voltam a ser encaminhadas ao upstream e o destaque da rota é removido

### Requirement: Inspeção de tráfego com waterfall

A interface SHALL listar as trocas capturadas e, ao selecionar uma, exibir a requisição e a resposta completas junto de uma representação temporal que separe visualmente o tempo consumido pelo upstream do tempo injetado pelo gateway. Trocas com intervenção MUST ser sinalizadas na lista, e a interface MUST permitir percorrer as trocas uma a uma a partir da que está aberta.

#### Scenario: Waterfall separa os tempos

- **WHEN** uma troca com latência injetada de `2s` sobre um upstream que respondeu em `150ms` é selecionada
- **THEN** a representação temporal exibe os dois períodos como segmentos distintos e rotulados

#### Scenario: Intervenção sinalizada na lista

- **WHEN** a lista contém trocas com erro do upstream e trocas sintetizadas por override
- **THEN** as sintetizadas são sinalizadas de forma distinta das demais

#### Scenario: Navegação item a item

- **WHEN** uma troca está aberta e a navegação para a seguinte é acionada
- **THEN** a interface abre a próxima troca respeitando o filtro ativo, sem voltar à listagem

#### Scenario: Requisição e resposta completas

- **WHEN** uma troca é selecionada
- **THEN** a interface exibe método, path, cabeçalhos e corpo de requisição e resposta, sinalizando corpos truncados

### Requirement: Indicação de histórico desabilitado

Quando a exposição ou o registro do histórico está desligado, a interface SHALL informar essa condição explicitamente em vez de apresentar uma lista vazia, distinguindo "desabilitado" de "nenhuma troca ainda".

#### Scenario: Exposição desligada

- **WHEN** a exposição do histórico está desligada e a inspeção de tráfego é aberta
- **THEN** a interface informa que o histórico está desabilitado e indica qual configuração o controla

#### Scenario: Histórico habilitado e vazio

- **WHEN** o histórico está habilitado e nenhuma troca ocorreu ainda
- **THEN** a interface informa que ainda não há trocas, sem sugerir que o recurso está desabilitado

### Requirement: Criar override a partir de uma chamada capturada

A interface SHALL permitir criar um override a partir de uma troca exibida na inspeção, apresentando os critérios e a resposta já preenchidos com os dados observados para revisão antes de passarem a valer.

#### Scenario: Override criado a partir da troca

- **WHEN** a criação de override é acionada sobre uma troca capturada
- **THEN** a interface apresenta um override pré-preenchido com os dados daquela requisição e daquela resposta, aguardando confirmação

#### Scenario: Override revisado antes de valer

- **WHEN** o override pré-preenchido é editado e confirmado
- **THEN** as requisições equivalentes seguintes passam a ser interceptadas pelo override editado

### Requirement: Atualização em tempo real

A interface SHALL refletir novas trocas e alterações de configuração sem que o usuário precise recarregar a página. Quando a conexão com o gateway é perdida, a interface MUST sinalizar a perda e MUST tentar restabelecê-la automaticamente.

#### Scenario: Nova troca aparece sozinha

- **WHEN** uma requisição atravessa o gateway com a interface aberta na inspeção de tráfego
- **THEN** a troca aparece na lista sem recarregar a página

#### Scenario: Perda de conexão sinalizada

- **WHEN** o processo do gateway se torna inacessível com a interface aberta
- **THEN** a interface sinaliza que está desconectada e volta a atualizar sozinha quando o gateway retorna
