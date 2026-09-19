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

### Requirement: Serviços em mapa e lista

A interface SHALL apresentar a configuração em duas visões do mesmo conjunto de serviços, em que cada serviço corresponde a uma rota e mostra seu nome, a entrada (o casamento de host e path pelo qual o app chama o gateway) e o destino (o upstream para onde o gateway redireciona). A visão padrão MUST ser o mapa, com o seu app, os serviços e os destinos em colunas ligadas; a lista densa MUST estar disponível como visão alternativa, e a escolha entre elas MUST ser lembrada entre sessões. A busca por nome, entrada ou destino e a ação de cadastrar um novo serviço MUST valer nas duas visões e estar sempre visíveis, qualquer que seja a seleção. As duas visões MUST continuar legíveis e operáveis com ao menos 50 serviços. Serviços com regra ativa MUST ser visualmente distinguíveis dos demais, e selecionar um serviço ou um destino MUST filtrar a inspeção de tráfego para ele.

#### Scenario: Topologia exibida

- **WHEN** existem três serviços apontando para dois destinos e o painel é aberto pela primeira vez
- **THEN** o mapa exibe o seu app ligado aos três serviços, cada serviço com sua entrada e ligado ao seu destino, e os dois destinos, um para cada grupo de serviços

#### Scenario: Lista como visão alternativa

- **WHEN** a visão em lista é escolhida e o painel é recarregado
- **THEN** a lista exibe os mesmos serviços, cada um com sua entrada e seu destino, e continua sendo a visão exibida

#### Scenario: Muitos serviços

- **WHEN** existem 50 serviços
- **THEN** o mapa e a lista os exibem sem sobreposição nem perda de legibilidade, com a coluna de serviços rolando dentro do painel e o seu app e os destinos ligados aos serviços à vista

#### Scenario: Busca nas duas visões

- **WHEN** existem 50 serviços e o nome de um deles é digitado na busca
- **THEN** a visão exibida passa a mostrar só os serviços que casam com o texto e, no mapa, só os destinos deles

#### Scenario: Novo serviço sempre alcançável

- **WHEN** um serviço está selecionado, no mapa ou na lista
- **THEN** a ação de cadastrar um novo serviço continua visível e abre o cadastro sem exigir desfazer a seleção

#### Scenario: Serviço com regra ativa é destacado

- **WHEN** um serviço possui regra ativa
- **THEN** ele é exibido com destaque visual que o diferencia dos demais, indicando o tipo de intervenção e a probabilidade aplicada

#### Scenario: Seleção filtra o tráfego

- **WHEN** um serviço é selecionado no mapa ou na lista
- **THEN** a inspeção de tráfego passa a exibir somente as trocas desse serviço, e o mapa recua o que não pertence ao caminho selecionado

#### Scenario: Destino indisponível é sinalizado

- **WHEN** um destino vem recusando conexões nas requisições recentes
- **THEN** o mapa sinaliza o destino e as ligações até ele como indisponíveis, e a lista sinaliza o destino dos serviços afetados

### Requirement: Vocabulário do usuário

A interface SHALL nomear os conceitos pelo que o desenvolvedor reconhece, e não pelos termos internos do gateway: "seu app" para quem chama, "serviço" para cada rota cadastrada, "entrada" para o casamento de host e path, "destino" para o upstream e "regra" para cada override. Os documentos em disco e a API de administração MAY manter os termos internos (rota, upstream, override).

#### Scenario: Termos internos fora da tela

- **WHEN** a interface é aberta com serviços e regras configurados
- **THEN** nenhum rótulo, título ou botão visível usa "rota", "upstream" ou "override" como nome desses conceitos


### Requirement: Controle direto do override na rota

A interface SHALL permitir ajustar probabilidade, latência e queda de conexão diretamente sobre o override, no painel do serviço selecionado no mapa ou na lista, por controle contínuo, aplicando a alteração imediatamente, sem etapa separada de confirmação ou salvamento. Quando o override possui tempo de vida ou limite de aplicações, a interface MUST exibir o restante de cada um.

#### Scenario: Ajuste aplicado imediatamente

- **WHEN** a probabilidade de um override é ajustada para 30% no controle contínuo
- **THEN** as requisições seguintes já seguem a nova proporção, sem nenhuma ação adicional de salvamento

#### Scenario: Restante de tempo e de aplicações exibido

- **WHEN** um override com tempo de vida de `60s` e limite de cinco aplicações está ativo
- **THEN** a interface exibe o tempo restante decrescendo e a contagem de aplicações, e remove o destaque da rota quando qualquer um se esgota

#### Scenario: Desligar o override em um gesto

- **WHEN** um override é desligado pela interface
- **THEN** as requisições seguintes voltam a ser encaminhadas ao upstream e o destaque da rota é removido

### Requirement: Modo simples e modo avançado

A interface SHALL oferecer dois modos de detalhe, com o modo simples como padrão. No modo simples, cada regra MUST mostrar apenas liga/desliga, nome, o efeito resumido (status sintetizado, latência ou queda) e a probabilidade; o modo avançado MUST revelar latência, queda, critérios de seleção, resposta declarada, tempo de vida e limite de aplicações, e os campos completos do serviço e do processo. A escolha do modo MUST ser lembrada entre visitas. Uma regra que usa recursos revelados só no avançado MUST continuar indicando isso no modo simples, para que nada fique escondido sem aviso.

#### Scenario: Simples é o padrão

- **WHEN** a interface é aberta pela primeira vez com um serviço selecionado
- **THEN** cada regra mostra apenas liga/desliga, nome, efeito e probabilidade

#### Scenario: Avançado revela o resto

- **WHEN** o modo avançado é acionado
- **THEN** latência, queda, critérios, resposta, tempo de vida e limite de aplicações passam a ser editáveis na mesma tela

#### Scenario: Modo lembrado

- **WHEN** o modo avançado é acionado e a página é recarregada
- **THEN** a interface volta no modo avançado

#### Scenario: Recurso avançado visível no simples

- **WHEN** uma regra declara latência e limite de aplicações e o modo simples está ativo
- **THEN** a regra indica esses ajustes no resumo, mesmo sem exibir seus controles

### Requirement: Tráfego enxuto

A lista de trocas SHALL mostrar por padrão apenas hora, método, path, status, tempo e a representação temporal, com a intervenção sinalizada junto do status. A coluna do serviço MUST aparecer somente quando a lista não está filtrada por um serviço. Os filtros MUST ficar recolhidos atrás de um único controle, e os filtros ativos MUST permanecer visíveis e removíveis um a um.

#### Scenario: Colunas enxutas

- **WHEN** a inspeção de tráfego é aberta filtrada por um serviço
- **THEN** a lista mostra hora, método, path, status, tempo e o waterfall, sem a coluna do serviço

#### Scenario: Filtros recolhidos

- **WHEN** a inspeção de tráfego é aberta sem nenhum filtro
- **THEN** os controles de filtro não ocupam a tela e ficam atrás de um único controle

#### Scenario: Filtro ativo permanece visível

- **WHEN** um filtro de faixa de status é aplicado e o controle de filtros é fechado
- **THEN** o filtro ativo continua visível e pode ser removido em um gesto

### Requirement: Edição com paridade aos documentos

A interface SHALL permitir consultar e alterar tudo o que `gateway.json` e os documentos de rota configuram, exclusivamente por meio da API de administração e somente por controles visuais. A interface MUST NOT exibir nem exigir a edição do conteúdo YAML ou JSON dos documentos: eles continuam sendo a fonte de verdade em disco, editáveis no editor do usuário, mas a interface opera sobre os valores. Valores definidos por variável de ambiente MUST aparecer travados, indicando a variável responsável.

#### Scenario: Controle grava no arquivo

- **WHEN** a latência de um override é alterada no controle visual
- **THEN** o arquivo em disco passa a declarar a nova latência, sem que a interface exiba o documento

#### Scenario: Edição externa refletida

- **WHEN** um documento de rota é editado fora da interface com um valor válido e a configuração é recarregada
- **THEN** os controles visuais refletem o novo valor sem recarregar a página

#### Scenario: Valor inválido é recusado

- **WHEN** um controle recebe um valor que a validação recusa
- **THEN** a interface mostra a mensagem de validação junto do controle e nada é gravado

#### Scenario: Valor do ambiente travado

- **WHEN** o backend do histórico vem de variável de ambiente
- **THEN** o controle correspondente aparece travado e indica o nome da variável

### Requirement: Liga e desliga do override e do aprendizado

A interface SHALL permitir ligar e desligar cada override e o modo aprendizado em um único gesto. Ajustar probabilidade, latência ou queda de um override desligado MUST ligá-lo no mesmo gesto. Os overrides aprendidos MUST ser distinguíveis dos declarados e MUST levar à troca de origem quando ela ainda estiver no histórico.

#### Scenario: Override aprendido vira caos em um gesto

- **WHEN** um override aprendido e desligado tem a probabilidade ajustada para 30%
- **THEN** ele passa a valer ligado com 30%, sem outra ação

#### Scenario: Modo aprendizado alternado na interface

- **WHEN** o modo aprendizado é ligado na interface e chega uma requisição para um path novo
- **THEN** o override aprendido aparece na rota sem recarregar a página

#### Scenario: Troca de origem acessível

- **WHEN** um override aprendido é aberto
- **THEN** a interface oferece a navegação para a troca capturada que o originou

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
