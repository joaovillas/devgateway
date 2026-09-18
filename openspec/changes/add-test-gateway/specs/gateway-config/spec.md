## Purpose

Mantém a configuração do processo em `gateway.json` e cada rota em seu próprio documento sob `routes/`, versionáveis e com chaves em inglês, para que o ambiente do time viva no repositório, possa ser revisado rota a rota e não produza conflito quando duas pessoas criam serviços diferentes.

## ADDED Requirements

### Requirement: Configuração do processo em gateway.json

O gateway SHALL carregar de `gateway.json` a configuração do processo: portas de tráfego e de administração, seed, backend e parâmetros de armazenamento do histórico, exposição e registro do histórico, limites de captura, modo aprendizado e o diretório de rotas. Todas as chaves MUST estar em inglês. Quando o arquivo não existe, o gateway MUST iniciar com os valores padrão e registrar um aviso identificando o caminho procurado.

#### Scenario: Configuração do processo carregada

- **WHEN** o gateway inicia com um `gateway.json` declarando as duas portas e o seed
- **THEN** o processo atende nessas portas e usa esse seed nas decisões probabilísticas

#### Scenario: Arquivo do processo ausente

- **WHEN** o gateway inicia e `gateway.json` não existe
- **THEN** o gateway sobe com os valores padrão, registra um aviso identificando o caminho e segue operacional

### Requirement: Um documento por rota

O gateway SHALL carregar cada rota de um documento YAML próprio dentro do diretório de rotas, com chaves em inglês, e MUST fundir todos os documentos encontrados num único snapshot. Cada documento MUST declarar a rota completa — upstream, casamento e overrides. Um documento inválido MUST impedir a carga, identificando o arquivo responsável, em vez de ser ignorado silenciosamente.

#### Scenario: Documentos fundidos num snapshot

- **WHEN** o diretório de rotas contém três documentos válidos
- **THEN** as três rotas atendem tráfego após a inicialização

#### Scenario: Diretório de rotas vazio ou ausente

- **WHEN** o diretório de rotas está vazio ou não existe
- **THEN** o gateway sobe sem nenhuma rota, registra um aviso e aceita rotas pela API de administração

#### Scenario: Documento inválido identifica o arquivo

- **WHEN** um dos documentos do diretório declara um campo inválido
- **THEN** a carga é recusada com uma mensagem que nomeia o arquivo, o campo e sua localização dentro dele

#### Scenario: Arquivo sem extensão reconhecida é ignorado

- **WHEN** o diretório de rotas contém um arquivo que não é um documento YAML
- **THEN** esse arquivo é ignorado sem impedir a carga dos demais

### Requirement: Detecção de colisão entre documentos

O gateway SHALL recusar a carga quando dois documentos de rota declaram o mesmo nome de rota, ou quando declaram o mesmo host e o mesmo padrão de path. A mensagem MUST nomear os dois arquivos em conflito.

#### Scenario: Nomes de rota duplicados

- **WHEN** dois documentos declaram rotas com o mesmo nome
- **THEN** a carga é recusada informando os dois arquivos e o nome repetido

#### Scenario: Padrões de casamento idênticos

- **WHEN** dois documentos declaram o mesmo host e o mesmo padrão de path
- **THEN** a carga é recusada informando os dois arquivos e o padrão em conflito

#### Scenario: Padrões distintos que se sobrepõem são permitidos

- **WHEN** um documento declara `/api/*` e outro declara `/api/payments/*`
- **THEN** ambos são carregados e a precedência por especificidade resolve o casamento

### Requirement: Validação da configuração

O gateway SHALL validar toda a configuração antes de aplicá-la e MUST recusar configuração inválida com uma mensagem que identifique o arquivo, o campo responsável e sua localização. O gateway MUST NOT iniciar com configuração inválida.

#### Scenario: Probabilidade fora do intervalo

- **WHEN** um documento de rota declara probabilidade `1.5` num override
- **THEN** a carga é recusada nomeando o arquivo, o campo e sua localização

#### Scenario: Upstream inválido

- **WHEN** um documento declara um upstream cujo endereço não é uma URL válida
- **THEN** a carga é recusada informando o arquivo e o valor rejeitado

#### Scenario: Portas iguais

- **WHEN** `gateway.json` declara a mesma porta para tráfego e administração
- **THEN** o gateway recusa iniciar informando o conflito

#### Scenario: Versão de schema superior à conhecida

- **WHEN** um documento declara uma versão de schema maior que a suportada pelo binário
- **THEN** a carga é recusada com uma mensagem que nomeia as duas versões

### Requirement: Variáveis de ambiente sobrepõem os arquivos

O gateway SHALL aceitar variáveis de ambiente para a configuração do processo, e estas MUST ter precedência sobre `gateway.json`, que por sua vez MUST ter precedência sobre os valores padrão. A origem efetiva de cada valor MUST ser consultável, de modo que o usuário saiba se um valor veio do ambiente, do arquivo ou do padrão.

#### Scenario: Ambiente vence o arquivo

- **WHEN** `gateway.json` declara o backend de histórico como memória e a variável de ambiente correspondente declara SQLite
- **THEN** o gateway usa SQLite

#### Scenario: Arquivo vence o padrão

- **WHEN** nenhuma variável de ambiente é definida e `gateway.json` declara uma porta de tráfego diferente da padrão
- **THEN** o gateway atende na porta declarada no arquivo

#### Scenario: Origem efetiva consultável

- **WHEN** a configuração efetiva é consultada com a porta vinda do ambiente e o seed vindo do arquivo
- **THEN** a consulta informa, para cada valor, se ele veio do ambiente, do arquivo ou do padrão

### Requirement: Recarga sem reinício

O gateway SHALL aplicar qualquer alteração de configuração — recarga dos arquivos ou alteração pela API — sem encerrar o processo nem derrubar conexões em andamento, inclusive alterações de porta e de backend do histórico. Quando a configuração nova é inválida ou não pode ser aplicada, o gateway MUST preservar a configuração anterior em vigor e reportar o erro.

#### Scenario: Recarga aplica a nova configuração

- **WHEN** um novo documento de rota é acrescentado ao diretório e a recarga é solicitada
- **THEN** a nova rota passa a atender sem que o processo seja reiniciado

#### Scenario: Recarga inválida preserva a configuração anterior

- **WHEN** um documento é editado com um valor inválido e a recarga é solicitada
- **THEN** a recarga é recusada com a mensagem de validação e as rotas anteriores continuam atendendo

#### Scenario: Requisições em andamento sobrevivem à recarga

- **WHEN** há requisições em curso e uma recarga válida é aplicada
- **THEN** as requisições em curso são concluídas sob a configuração que as iniciou

#### Scenario: Porta trocada a quente

- **WHEN** a porta de tráfego é alterada para uma porta livre com requisições em curso na porta atual
- **THEN** o gateway passa a atender na porta nova, deixa de aceitar conexões na antiga e conclui as requisições em curso, sem reiniciar o processo

#### Scenario: Porta nova indisponível preserva a atual

- **WHEN** a porta de tráfego é alterada para uma porta já ocupada
- **THEN** a alteração é recusada informando a porta e a causa, e o gateway segue atendendo na porta atual

#### Scenario: Backend do histórico trocado a quente

- **WHEN** o backend do histórico é alterado de memória para SQLite com o gateway em execução
- **THEN** o SQLite é inicializado antes da troca, as trocas seguintes passam a ser registradas nele e o histórico anterior não é migrado

#### Scenario: Backend novo indisponível preserva o atual

- **WHEN** o backend do histórico é alterado para um que não pode ser inicializado
- **THEN** a alteração é recusada informando o backend e a causa, e o backend atual segue em uso

### Requirement: API de administração

O gateway SHALL expor uma API de administração que permita consultar e alterar tudo o que `gateway.json` e os documentos de rota configuram — rotas, overrides e configuração do processo — e consultar o histórico de tráfego. Uma alteração de rota MUST reescrever apenas o documento daquela rota, deixando os demais intactos, e uma alteração do processo MUST ser gravada em `gateway.json`. Um valor definido por variável de ambiente MUST NOT ser alterável pela API, e a recusa MUST nomear a variável responsável. Escritas concorrentes MUST ser serializadas de modo que nenhuma seja perdida, e a escrita de cada documento MUST ser atômica.

#### Scenario: Alteração atinge apenas o documento da rota

- **WHEN** um override é acrescentado à rota `payments` pela API e os documentos do diretório são lidos em seguida
- **THEN** somente `payments` foi reescrito e os demais documentos permanecem byte a byte iguais

#### Scenario: Criação de rota gera documento próprio

- **WHEN** uma rota nova é criada pela API
- **THEN** um documento correspondente passa a existir no diretório de rotas

#### Scenario: Escritas concorrentes são serializadas

- **WHEN** duas alterações em rotas diferentes são submetidas simultaneamente
- **THEN** ambas são aplicadas e os dois documentos refletem as alterações

#### Scenario: Configuração do processo alterada pela API

- **WHEN** o seed é alterado pela API
- **THEN** o novo seed passa a valer sem reinício e `gateway.json` passa a declará-lo

#### Scenario: Valor do ambiente é travado

- **WHEN** a porta de tráfego vem de variável de ambiente e a API recebe uma alteração dessa porta
- **THEN** a alteração é recusada nomeando a variável de ambiente responsável, e `gateway.json` não é modificado

#### Scenario: Alteração inválida é recusada sem tocar o disco

- **WHEN** a API recebe um override com latência mínima maior que a máxima
- **THEN** a alteração é recusada com a mensagem de validação e nenhum documento é modificado

### Requirement: Separação entre porta de tráfego e porta de administração

O gateway SHALL atender o tráfego encaminhado e a administração em portas distintas. A API de administração e a interface MUST NOT ser acessíveis pela porta de tráfego, e nenhum path reservado MUST ser subtraído do espaço de rotas do usuário.

#### Scenario: Administração indisponível na porta de tráfego

- **WHEN** uma requisição para um path da API de administração chega pela porta de tráfego
- **THEN** ela é tratada como tráfego comum, sujeita à resolução de rotas configurada pelo usuário

#### Scenario: Tráfego indisponível na porta de administração

- **WHEN** uma requisição para um path de rota configurada chega pela porta de administração
- **THEN** ela não é encaminhada a nenhum upstream
