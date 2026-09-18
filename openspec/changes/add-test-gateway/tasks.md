## 1. Fundação do projeto

- [x] 1.1 Inicializar o módulo Go e o layout `cmd/gateway`, `internal/...`, `web/`, e verificar que `go build ./...` conclui em um clone limpo
- [x] 1.2 Versionar um `web/dist/index.html` mínimo e a diretiva `//go:embed all:web/dist`, e verificar que `go build ./...` conclui sem que o frontend tenha sido construído
- [x] 1.3 Criar o `Makefile` (ou `Taskfile`) com alvos de build, teste e lint, e verificar que cada alvo roda do zero
- [ ] 1.4 Configurar CI rodando build, `go vet` e testes com `-race`, e verificar que o pipeline passa no primeiro commit

## 2. Configuração

- [x] 2.1 Definir as estruturas de `gateway.json` (portas, seed, armazenamento, exposição e registro do histórico, limites de captura, diretório de rotas, versão de schema) com chaves em inglês, e verificar por teste de serialização que um arquivo de exemplo carrega e reserializa sem perda de campos
- [x] 2.2 Definir a estrutura do documento de rota (nome, upstream, casamento, overrides, versão de schema) com chaves em inglês, e verificar por teste que um documento de exemplo carrega e reserializa sem perda
- [x] 2.3 Implementar a varredura do diretório de rotas e a fusão num snapshot, ignorando arquivos sem extensão reconhecida, e verificar pelos quatro cenários da requirement "Um documento por rota"
- [x] 2.4 Implementar a detecção de colisão entre documentos (nome repetido, host e padrão idênticos), e verificar pelos três cenários da requirement correspondente
- [x] 2.5 Implementar a validação com mensagem nomeando arquivo, campo e localização, incluindo a recusa por versão de schema superior, e verificar pelos quatro cenários da requirement de validação
- [x] 2.6 Implementar a precedência ambiente sobre arquivo sobre padrão e a consulta de origem efetiva de cada valor, e verificar pelos três cenários da requirement correspondente
- [x] 2.7 Implementar o snapshot imutável atrás de `atomic.Pointer` com índices pré-computados, e verificar por teste de concorrência com `-race` que leituras simultâneas à troca nunca observam estado parcial
- [ ] 2.8 Implementar a inicialização com portas separadas e recusa de portas iguais, e verificar pelos dois cenários da requirement de separação de portas e pelo cenário "Portas iguais" da validação

## 3. Roteamento reverso

- [x] 3.1 Implementar a resolução de rota por lista ordenada com curinga de sufixo e path exato (host antes de path, mais específico antes de menos), e verificar pelos cenários de precedência das requirements de roteamento por curinga e por host
- [x] 3.2 Implementar o encaminhamento sobre `httputil.ReverseProxy` com remoção opcional de prefixo, e verificar pelos cenários de remoção e preservação de prefixo
- [x] 3.3 Implementar os cabeçalhos de encaminhamento com acúmulo de `X-Forwarded-For` e preservação opcional do `Host`, e verificar pelos três cenários da requirement de encaminhamento de cabeçalhos
- [x] 3.4 Implementar as respostas de falha do upstream (`502` sem conexão, `504` por tempo limite, `404` sem rota) com corpo diagnóstico, e verificar pelos cenários correspondentes
- [x] 3.5 Verificar a transparência do tráfego com um teste de ponta a ponta que exercita método e corpo preservados, status repassado e resposta `text/event-stream` chegando incrementalmente

## 4. Armazenamento do histórico

- [ ] 4.1 Definir a interface de armazenamento (registrar, listar com filtros, buscar por identificador, navegar por cursor, limpar) e a bateria de testes de contrato que roda contra qualquer implementação, e verificar que a bateria falha contra uma implementação vazia
- [ ] 4.2 Implementar o backend em memória como anel de capacidade configurável, e verificar pela bateria de contrato e pelo cenário "Capacidade excedida em memória"
- [ ] 4.3 Implementar o backend NDJSON com escrita append e leitura indexada, e verificar pela bateria de contrato e pelo cenário de sobrevivência ao reinício
- [ ] 4.4 Implementar o backend SQLite com driver puro em Go, e verificar pela bateria de contrato, pelo cenário de sobrevivência ao reinício e por `go build` com `CGO_ENABLED=0` concluindo
- [ ] 4.5 Implementar a seleção do backend por variável de ambiente com memória como padrão e recusa de iniciar quando o backend falha, e verificar pelos cenários "Memória é o padrão" e "Backend indisponível impede a inicialização"

## 5. Captura de tráfego

- [ ] 5.1 Implementar o registro da troca com identificador único, dados de requisição e resposta, rota, override e tamanhos, com truncamento de corpo no limite configurado, e verificar pelos três cenários da requirement de registro
- [ ] 5.2 Implementar a cronometragem decomposta em tempo total, tempo de upstream, tempo injetado e overhead do gateway, e verificar pelos três cenários da requirement de decomposição da latência
- [ ] 5.3 Implementar a consulta com ordem cronológica inversa, paginação e filtros combinados, e verificar pelos quatro cenários da requirement de consulta e filtragem
- [ ] 5.4 Implementar a leitura por identificador e a navegação item a item respeitando os filtros ativos, e verificar pelos quatro cenários da requirement de leitura individual e navegação por cursor
- [ ] 5.5 Implementar o desligamento independente de registro e de exposição, e a limpeza sob demanda, e verificar pelos três cenários da requirement de exposição configurável

## 6. Overrides

- [ ] 6.1 Implementar os critérios de seleção (path exato, curinga e regex; método, cabeçalhos, query e corpo com os operadores de igualdade, regex, igualdade JSON e substring), e verificar pelos cinco cenários da requirement de critérios de seleção
- [ ] 6.2 Implementar a precedência por especificidade entre overrides, com desempate pela ordem de declaração, e verificar pelos três cenários da requirement de precedência
- [ ] 6.3 Implementar o passthrough por padrão e o `501` de rota sem upstream e sem override casado, e verificar pelos três cenários da requirement de interceptação seletiva
- [ ] 6.4 Implementar a resposta declarada com status padrão `200` e inferência do tipo de conteúdo para corpo JSON, e verificar pelos três cenários da requirement de resposta declarada
- [ ] 6.5 Implementar a fonte de aleatoriedade derivada de `(seed, número de sequência)` com `math/rand/v2`, e verificar pelos dois cenários da requirement de determinismo, com `-race` e requisições concorrentes
- [ ] 6.6 Implementar a probabilidade de aplicação com passthrough quando não sorteada, e verificar pelos quatro cenários da requirement correspondente, incluindo o teste estatístico de 1000 requisições a 30%
- [ ] 6.7 Implementar a latência fixa ou sorteada em intervalo, aplicada após a resposta estar pronta, inclusive no caso de override sem `respond`, e verificar pelos cinco cenários da requirement de latência e queda
- [ ] 6.8 Implementar a queda de conexão via `http.Hijacker` com degradação documentada em HTTP/2, e verificar pelo cenário "Queda encerra sem resposta" e pelo cenário de queda da spec `traffic-capture`
- [ ] 6.9 Implementar a expiração por tempo de vida e por contagem de aplicações, com ambos consultáveis, e verificar pelos quatro cenários da requirement de expiração
- [ ] 6.10 Implementar o cabeçalho de identificação da intervenção e a marcação na troca capturada, e verificar pelos dois cenários da requirement de identificação e pelos três cenários da requirement de distinção da spec `traffic-capture`

## 7. API de administração

- [ ] 7.1 Implementar os endpoints de leitura e escrita de rotas e overrides, e verificar por testes de API que cada recurso é criado, alterado, lido e removido
- [ ] 7.2 Implementar a persistência por documento com escrita atômica e mutex de escrita, e verificar pelos quatro cenários da requirement de API de administração, incluindo a comparação byte a byte dos documentos não tocados
- [ ] 7.3 Implementar a recarga sob comando preservando a configuração anterior em caso de erro e recusando alterar portas, e verificar pelos quatro cenários da requirement de recarga sem reinício
- [ ] 7.4 Implementar os endpoints do histórico — listagem com filtros, leitura por identificador, navegação por cursor e limpeza — e verificar por teste de API que cada um respeita a configuração de exposição
- [ ] 7.5 Implementar o endpoint de configuração efetiva com a origem de cada valor, e verificar pelo cenário "Origem efetiva consultável"
- [ ] 7.6 Implementar a derivação de override a partir de uma troca capturada, incluindo a recusa para troca inexistente e o tratamento de corpo truncado, e verificar pelos três cenários da requirement de derivação
- [ ] 7.7 Implementar o fluxo SSE de trocas novas com agregação de no máximo uma atualização por segundo, e verificar por teste que um cliente conectado recebe as trocas em ordem e que a conexão sobrevive a períodos sem tráfego
- [ ] 7.8 Escrever a referência da API e verificar que cada operação das specs tem um exemplo executável por `curl`

## 8. Interface web

- [ ] 8.1 Montar o projeto Vite com React e TypeScript, o cliente da API e o build para `web/dist`, e verificar que o binário construído serve a interface na porta de administração sem acesso à rede externa, conforme os dois cenários da requirement de interface servida pelo próprio binário
- [ ] 8.2 Implementar o mapa de topologia com ligações entre cliente, rota e upstream, destaque de override ativo, sinalização de upstream indisponível e filtragem por seleção, e verificar pelos quatro cenários da requirement de mapa de topologia
- [ ] 8.3 Implementar os controles contínuos do override aplicados imediatamente, com restante de tempo e de aplicações e desligamento em um gesto, e verificar pelos três cenários da requirement de controle direto
- [ ] 8.4 Implementar a lista e o detalhe de tráfego com waterfall separando tempo de upstream e tempo injetado, sinalização de intervenção, navegação item a item e exibição de corpos truncados, e verificar pelos quatro cenários da requirement de inspeção com waterfall
- [ ] 8.5 Implementar a indicação de histórico desabilitado distinta de histórico vazio, e verificar pelos dois cenários da requirement correspondente
- [ ] 8.6 Implementar a criação de override a partir de uma troca exibida, com revisão antes de valer, e verificar pelos dois cenários da requirement correspondente
- [ ] 8.7 Implementar o consumo do fluxo SSE com sinalização de desconexão e reconexão automática, e verificar pelos dois cenários da requirement de atualização em tempo real
- [ ] 8.8 Implementar o aviso de perda de comentários antes da primeira escrita num documento de rota que os contenha, e verificar que o aviso aparece uma vez e não reaparece após a confirmação

## 9. Distribuição e documentação

- [ ] 9.1 Produzir binários por plataforma e a imagem Docker no mesmo build que embute o frontend, e verificar que a imagem sobe e atende nas duas portas
- [ ] 9.2 Escrever o README com instalação, `gateway.json` e documento de rota de exemplo, as variáveis de ambiente, o critério dos dois formatos, e os limites assumidos (perda de comentários na escrita, determinismo por ordem de chegada, queda de conexão em HTTP/2), e verificar que um leitor consegue subir o gateway seguindo apenas o documento
- [ ] 9.3 Montar um ambiente de exemplo com dois serviços de brinquedo e documentos de rota prontos, e verificar de ponta a ponta que passthrough, override forçado, override probabilístico, latência em intervalo e waterfall funcionam sobre ele
