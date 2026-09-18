## Purpose

Recebe todo o tráfego de desenvolvimento numa porta única e encaminha cada requisição ao serviço upstream correto, para que os clientes deixem de conhecer as portas individuais de cada serviço.

## ADDED Requirements

### Requirement: Roteamento por curinga de path

O gateway SHALL encaminhar cada requisição para o upstream da rota cujo padrão de path casa com ela. O padrão MUST aceitar a forma de prefixo com curinga de sufixo, como `/api/payments/*`, e a forma de path exato. Quando mais de uma rota casa, o gateway MUST escolher a de padrão mais específico — path exato antes de curinga, e curinga mais longo antes de curinga mais curto. Cada rota MAY declarar a remoção do prefixo antes do encaminhamento.

#### Scenario: Curinga mais específico vence

- **WHEN** existem as rotas `/api/*` e `/api/payments/*` e chega uma requisição para `/api/payments/123`
- **THEN** a requisição é encaminhada para o upstream da rota `/api/payments/*`

#### Scenario: Remoção do prefixo antes do encaminhamento

- **WHEN** a rota `/api/payments/*` está configurada para remover o prefixo e chega uma requisição para `/api/payments/123`
- **THEN** o upstream recebe a requisição no path `/123`

#### Scenario: Prefixo preservado por padrão

- **WHEN** a rota não declara remoção de prefixo e chega uma requisição para `/api/payments/123`
- **THEN** o upstream recebe a requisição no path `/api/payments/123`

#### Scenario: Nenhuma rota casa

- **WHEN** chega uma requisição para um path que nenhuma rota cobre
- **THEN** o gateway responde `404` com um corpo que informa que nenhuma rota casou e lista os padrões configurados

### Requirement: Roteamento por host

O gateway SHALL permitir que uma rota exija um host específico, casando com o cabeçalho `Host` da requisição. Uma rota MAY combinar host e padrão de path, e nesse caso ambos os critérios MUST casar.

#### Scenario: Roteamento apenas por host

- **WHEN** existe uma rota para o host `payments.local` e chega uma requisição com `Host: payments.local`
- **THEN** a requisição é encaminhada para o upstream dessa rota

#### Scenario: Host e path combinados

- **WHEN** existe uma rota para o host `payments.local` com padrão `/v2/*` e chega uma requisição com `Host: payments.local` para o path `/v1/charge`
- **THEN** essa rota não casa e a requisição segue a resolução das demais rotas

#### Scenario: Rota com host tem precedência sobre rota sem host

- **WHEN** uma rota com host e uma rota apenas de path casam com a mesma requisição
- **THEN** a rota com host é escolhida

### Requirement: Encaminhamento de cabeçalhos

O gateway SHALL acrescentar `X-Forwarded-For`, `X-Forwarded-Proto` e `X-Forwarded-Host` às requisições encaminhadas. Por padrão o gateway MUST substituir o cabeçalho `Host` pelo host do upstream; uma rota MAY optar por preservar o `Host` original.

#### Scenario: Cabeçalhos de encaminhamento acrescentados

- **WHEN** uma requisição é encaminhada para um upstream
- **THEN** o upstream recebe `X-Forwarded-For` com o endereço do cliente, `X-Forwarded-Proto` com o esquema original e `X-Forwarded-Host` com o host original

#### Scenario: Host original preservado sob demanda

- **WHEN** a rota declara preservação do host e chega uma requisição com `Host: payments.local`
- **THEN** o upstream recebe `Host: payments.local` em vez do host do upstream

#### Scenario: X-Forwarded-For acumula a cadeia

- **WHEN** a requisição já chega com `X-Forwarded-For` preenchido
- **THEN** o gateway acrescenta o endereço do cliente ao valor existente em vez de substituí-lo

### Requirement: Tratamento de falha do upstream

O gateway SHALL responder `502` quando não conseguir estabelecer conexão com o upstream e `504` quando o upstream exceder o tempo limite configurado para a rota. O corpo da resposta MUST identificar a rota e o upstream envolvidos.

#### Scenario: Upstream recusa a conexão

- **WHEN** o upstream de uma rota não está aceitando conexões e chega uma requisição para ela
- **THEN** o gateway responde `502` com um corpo que nomeia a rota e o endereço do upstream

#### Scenario: Upstream excede o tempo limite

- **WHEN** o upstream demora mais que o tempo limite configurado para responder
- **THEN** o gateway responde `504` e encerra a requisição ao upstream

#### Scenario: Falha do upstream não derruba o gateway

- **WHEN** um upstream falha repetidamente
- **THEN** as demais rotas continuam sendo atendidas normalmente

### Requirement: Transparência do tráfego encaminhado

O gateway SHALL encaminhar qualquer método HTTP, preservando corpo, cabeçalhos de entrada e códigos de status de saída sem alteração, salvo os cabeçalhos de encaminhamento e as intervenções declaradas por override. Respostas em streaming MUST ser repassadas de forma incremental, sem aguardar o corpo completo.

#### Scenario: Método e corpo preservados

- **WHEN** chega um `POST` com corpo binário e cabeçalho `Content-Type` específico
- **THEN** o upstream recebe o mesmo método, o mesmo corpo byte a byte e o mesmo `Content-Type`

#### Scenario: Status do upstream repassado

- **WHEN** o upstream responde `418`
- **THEN** o cliente recebe `418` com os mesmos cabeçalhos e corpo

#### Scenario: Resposta em streaming repassada incrementalmente

- **WHEN** o upstream responde com `text/event-stream` e emite eventos ao longo do tempo
- **THEN** o cliente recebe cada evento conforme ele é emitido, sem esperar o encerramento da resposta
