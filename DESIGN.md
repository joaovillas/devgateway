---
name: gateway
description: Painel de controle do gateway de desenvolvimento, um console escuro de painéis fixos onde cor é sempre estado.
colors:
  ground: "#0d1117"
  panel: "#151a22"
  panel-raised: "#1b212b"
  seam: "#262e3a"
  seam-strong: "#354050"
  ink: "#d8dee7"
  ink-2: "#a3aebb"
  ink-3: "#808c9b"
  ink-bright: "#ffffff"
  override: "#5aa2ff"
  override-dim: "rgb(90 162 255 / 0.14)"
  injected: "#e5a53d"
  fault: "#f26b64"
  fault-dim: "rgb(242 107 100 / 0.14)"
  healthy: "#4cc26a"
  time-upstream: "#6d7f96"
  time-gateway: "#3e4a5a"
typography:
  panel-title:
    fontFamily: "Source Sans 3 Variable, Segoe UI, system-ui, sans-serif"
    fontSize: "0.9375rem"
    fontWeight: 600
    letterSpacing: "0.1em"
    fontFeature: "font-variant-caps: all-small-caps"
  label:
    fontFamily: "Source Sans 3 Variable, Segoe UI, system-ui, sans-serif"
    fontSize: "0.9375rem"
    fontWeight: 600
    letterSpacing: "0.08em"
    fontFeature: "font-variant-caps: all-small-caps"
  title:
    fontFamily: "Source Sans 3 Variable, Segoe UI, system-ui, sans-serif"
    fontSize: "0.9375rem"
    fontWeight: 600
  body:
    fontFamily: "Source Sans 3 Variable, Segoe UI, system-ui, sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 400
    lineHeight: 1.4
    fontFeature: "tnum"
  data:
    fontFamily: "JetBrains Mono Variable, ui-monospace, Cascadia Mono, Consolas, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    fontFeature: "tnum, zero"
  document:
    fontFamily: "JetBrains Mono Variable, ui-monospace, Cascadia Mono, Consolas, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: 1.6
rounded:
  sm: "2px"
spacing:
  s-1: "2px"
  s-2: "4px"
  s-3: "8px"
  s-4: "12px"
  s-5: "16px"
  bar-h: "30px"
  panel-head-h: "30px"
  row-h: "26px"
components:
  button:
    backgroundColor: "{colors.panel-raised}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sm}"
    padding: "0 12px"
    height: "24px"
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.ground}"
    rounded: "{rounded.sm}"
    padding: "0 12px"
    height: "24px"
  button-primary-hover:
    backgroundColor: "{colors.ink-bright}"
    textColor: "{colors.ground}"
  button-sm:
    backgroundColor: "{colors.panel-raised}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sm}"
    padding: "0 8px"
    height: "22px"
  icon-button:
    textColor: "{colors.ink-3}"
    rounded: "{rounded.sm}"
    size: "22px"
  icon-button-hover:
    backgroundColor: "{colors.panel-raised}"
    textColor: "{colors.ink}"
  input:
    backgroundColor: "{colors.ground}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sm}"
    padding: "0 8px"
    height: "24px"
  chip:
    textColor: "{colors.ink-2}"
    typography: "{typography.data}"
    rounded: "{rounded.sm}"
    padding: "0 8px"
    height: "20px"
  chip-override:
    backgroundColor: "{colors.override-dim}"
    textColor: "{colors.override}"
    rounded: "{rounded.sm}"
  panel:
    backgroundColor: "{colors.panel}"
    textColor: "{colors.ink}"
  panel-head:
    textColor: "{colors.ink-2}"
    typography: "{typography.panel-title}"
    padding: "0 12px"
    height: "30px"
  map-node:
    backgroundColor: "{colors.panel-raised}"
    textColor: "{colors.ink}"
    rounded: "{rounded.sm}"
    padding: "0 8px"
  map-node-selected:
    backgroundColor: "{colors.override-dim}"
    textColor: "{colors.ink}"
  traffic-row:
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    padding: "0 8px"
    height: "26px"
  traffic-row-open:
    backgroundColor: "{colors.override-dim}"
---

# Design System: gateway

## Overview

**Creative North Star: "A Bancada de Instrumentos"**

O painel é um console de painéis fixos sobre um chão grafite: mapa, tráfego, detalhe e documento, cada um com o seu lugar, separados por costuras de 1px e nunca empilhados em camadas. É uma bancada que o desenvolvedor consulta de relance numa segunda tela, então a densidade é alta, a tinta é quase toda neutra e a atenção vai para o único sinal que importa: o que o gateway está fazendo com o tráfego.

A cor não decora nada. Azul, âmbar, vermelho e verde são quatro estados com significado fixo, e todo o resto se resolve em tons de grafite e tinta. Números são sempre tabulares, o dado é mono e os cabeçalhos são versalete discreto. A profundidade vem do tom (chão, painel, painel elevado), nunca de sombra. O movimento se limita a dois momentos: a troca que acabou de chegar e a linha do documento que o controle ao lado acabou de mudar.

**Key Characteristics:**
- Painéis fixos em grade, separados por costuras de 1px na cor `seam` (o `gap: 1px` sobre fundo `seam`).
- Cor exclusivamente semântica: azul seleção/override, âmbar injetado, vermelho queda/sintetizado, verde saudável.
- Dados em JetBrains Mono 12px com algarismos tabulares e zero cortado; interface em Source Sans 3 a 13px.
- Cantos quase retos (2px) e controles baixos (22 a 26px de altura).
- Nenhuma sombra de elevação; estados de foco e seleção são traços de 1px.

## Colors

Grafite frio em três degraus de superfície, tinta em três degraus de contraste e quatro cores de estado que só aparecem quando há algo a dizer.

### Primary
- **Azul de Seleção** (`override`): tudo o que está selecionado, aberto ou sob override: borda do nó selecionado, aresta do caminho escolhido no mapa, linha aberta no tráfego, interruptor ligado, filtro ativo, anel de foco, cursor e traço de mudança no documento. Sua versão translúcida (`override-dim`) é o fundo de seleção, de texto selecionado e do brilho de chegada.

### Secondary
- **Âmbar de Injeção** (`injected`): tempo e atrasos injetados pelo gateway: o segmento do waterfall, a etiqueta de atraso, o medidor sob o nó e o trilho do controle de latência.

### Tertiary
- **Vermelho de Queda** (`fault`): o que o gateway derrubou ou sintetizou, e upstream indisponível: etiquetas, medidor do nó, aresta tracejada para upstream fora, status sintetizado. `fault-dim` tinge a barra superior inteira quando a conexão com o painel cai e o fundo da marca de status no estreito.
- **Verde Saudável** (`healthy`): somente o ponto de estado de upstream e conexão saudáveis. Nunca preenche áreas.

### Neutral
- **Chão Grafite** (`ground`): o fundo sob tudo, e o miolo recuado de campos, blocos de código e trilhos do waterfall.
- **Painel** (`panel`): a superfície de cada painel fixo, da barra superior e dos cabeçalhos colantes.
- **Painel Elevado** (`panel-raised`): nós do mapa, botões, hover de linha, avisos de estado, rascunhos e o documento com alterações pendentes.
- **Costura** (`seam`): as linhas de 1px entre painéis, linhas de tabela, divisões de seção. **Costura Forte** (`seam-strong`): contornos de controles, arestas do mapa em repouso, barra de rolagem.
- **Tinta** (`ink`), **Tinta 2** (`ink-2`), **Tinta 3** (`ink-3`): texto primário, secundário (títulos de painel, chaves do YAML) e terciário (rótulos, metadados, gutter). `ink-3` é o piso: mantém 4.5:1 sobre `panel`, e nada legível fica abaixo dele.
- **Tinta Plena** (`ink-bright`): só o hover de elementos já em tinta (botão principal, polegar do controle contínuo, link da barra). Hoje está literal no CSS; ao mexer, promova a variável.
- **Tempo do Upstream** (`time-upstream`) e **Tempo do Gateway** (`time-gateway`): os segmentos neutros do waterfall, para que só o âmbar injetado salte.

### Named Rules
**A Regra da Cor Que Significa.** Azul, âmbar, vermelho e verde só aparecem para o estado que nomeiam. Um botão, um título ou uma falha de leitura do próprio painel não ganham cor de estado: o botão principal é tinta cheia e o erro de leitura tem título em `ink`.

**A Regra do Vermelho do Tráfego.** Vermelho é o que o gateway fez com o tráfego (queda, sintetizado) ou upstream fora. Erro vindo do upstream é tinta com sublinhado pontilhado; erro do próprio gateway é `ink-2`. Nenhum dos dois rouba o vermelho.

**A Regra do Recuo por Tom.** Fora da seleção, o que recua perde cor de estado e desce para `panel`, `seam` e `ink-3`, sem cair abaixo do contraste legível. Opacidade só é usada em arestas do mapa (0.3) e em controles desabilitados (0.5).

## Typography

**Display Font:** nenhuma; o console não tem display.
**Body Font:** Source Sans 3 Variable (com Segoe UI, system-ui)
**Label/Mono Font:** JetBrains Mono Variable (com ui-monospace, Cascadia Mono, Consolas)

**Character:** uma sans humanista e compacta para a interface e uma mono de engenharia para tudo o que é dado. Ambas vêm embutidas no binário; nada é carregado da rede.

### Hierarchy
- **Título de painel** (600, 15px em versalete, espaçamento 0.1em, `ink-2`): o nome de cada painel no cabeçalho de 30px, as abas e os títulos de seção dentro do painel da rota. Versalete encolhe para a altura-x, por isso o tamanho nominal é 15px.
- **Rótulo** (600 ou 400, 15px em versalete, espaçamento 0.08em, `ink-3`): cabeçalhos de coluna do tráfego e do mapa, rótulos da barra superior e dos filtros, grupos de formulário. Sempre rotula o controle ou dado ao lado dele.
- **Título** (600, 15px): o nome do override e, em mono, a requisição e o status no detalhe da troca.
- **Corpo** (400, 13px, 1.4): todo o texto de interface. Textos corridos de estado e aviso param em 62 a 64ch.
- **Dado** (400, 12px mono, tabular e zero cortado): métodos, paths, status, durações, probabilidades, portas, etiquetas e campos numéricos.
- **Documento** (400, 12px mono, 1.6, tab de 2): o YAML ao vivo e os corpos de mensagem (1.55).
- **Micro** (11px mono): apenas atalhos de teclado e marcas do eixo do waterfall.

### Named Rules
**A Regra do Número Tabular.** O corpo inteiro roda com `tabular-nums`; o dado mono ainda acrescenta o zero cortado. Coluna de número alinha à direita.

**A Regra do Dado em Mono.** Se o valor vai para um arquivo ou vem de uma requisição, ele é mono. Se é frase para o humano, é sans.

## Layout

O console ocupa a viewport inteira sem rolagem de página: uma barra de 30px e, abaixo, duas colunas em 3fr/2fr (60/40). A coluna esquerda empilha mapa (até metade da altura, ajustado ao que desenha) e tráfego (o resto); a direita divide controles da rota e documento meio a meio, ou 2fr/1fr quando o detalhe de uma troca está aberto. Os painéis se separam por `gap: 1px` sobre o fundo `seam`, e cada corpo de painel rola sozinho.

O ritmo usa a escala de 2, 4, 8, 12 e 16px. Recuo lateral de painel é 12px; controles e células usam 8px; seções dentro de um painel se separam por 16px e uma costura. Linhas de formulário são grade de rótulo de 108px e controle; configurações do processo são grade de rótulo, controle e origem.

Abaixo de 900px os painéis empilham e a página rola; só a lista de tráfego mantém rolagem própria (até 70vh). Abaixo de 640px o mapa encolhe as colunas e troca nomes longos por curtos, o tráfego perde as colunas de hora, rota e intervenção, e o status sintetizado ganha uma caixa para não depender só da cor.

## Elevation & Depth

O sistema é plano. A profundidade vem de três tons de superfície (`ground` < `panel` < `panel-raised`) e de costuras de 1px. Não existe sombra projetada em lugar nenhum. O `box-shadow` só aparece como traço interno de 1px: o sublinhado da opção ativa no seletor segmentado e as bordas superior e inferior azuis da linha aberta ou focada no tráfego. O ponto ocioso usa o mesmo recurso como contorno.

### Named Rules
**A Regra da Costura, Não da Sombra.** Separação é costura de 1px ou mudança de tom. Sombra desfocada ou deslocada não pertence a este mundo; traço interno de 1px, sim.

**A Regra do Colante Opaco.** Cabeçalhos colantes (tabela, barra do documento, barra do detalhe, aviso de comentários) têm fundo opaco de `panel` ou `panel-raised` e uma costura embaixo; o conteúdo passa por trás, nunca através.

## Shapes

Quase tudo é retangular com cantos de 2px: botões, campos, chips, nós do mapa, blocos de código, rascunhos. As exceções têm função: o ponto de estado é círculo de 7px, o interruptor é pílula (raio 8px) com botão circular, e o polegar do controle contínuo é uma barra vertical de 8×16px com canto de 1px sobre trilho de 2px.

O traço carrega significado. Contínuo é o normal; tracejado quer dizer "não é seu para mudar agora" ou "incompleto": campo travado pelo ambiente, seletor travado, nó fantasma, procedência aprendida ou derivada, corpo cortado na captura, aresta para upstream fora. Pontilhado sob texto marca erro do upstream.

## Components

### Buttons
Discretos e baixos; nenhum usa cor de estado.
- **Shape:** retângulo de canto quase reto (2px), 24px de altura, 22px no tamanho pequeno.
- **Padrão:** `panel-raised` com contorno `seam-strong` e tinta `ink`; hover leva o contorno a `ink-3`.
- **Principal:** tinta cheia (`ink`) com texto `ground` em 600; hover vai para `ink-bright`. Um por contexto.
- **Texto:** sem caixa, `ink-2`, hover em `ink` com sublinhado de 1px. A variante destrutiva é `fault`, reservada para apagar override ou rota.
- **Ícone:** 22px quadrado, `ink-3`, hover com fundo `panel-raised`.
- **Foco:** contorno de 1px `override` com recuo de 1px. Desabilitado cai para 50% de opacidade.

### Chips
- **Style:** 20px de altura, contorno `seam-strong`, dado em mono `ink-2`.
- **State:** o chip de override ativo (ex. filtro de rota) ganha contorno, texto e fundo azuis e um botão de fechar interno.

### Cards / Containers
O console não tem cards. Os contêineres são o **painel** (fundo `panel`, cabeçalho de 30px com título em versalete, subtítulo `ink-3` e ferramentas à direita, costura embaixo) e as **molduras internas**: aviso de erro de escrita, rascunho derivado e formulário de criação, todos com contorno `seam-strong`, canto de 2px e recuo de 12px, sobre `panel-raised` quando precisam se destacar do painel.

### Inputs / Fields
- **Style:** 24px de altura (22 no pequeno), fundo `ground` recuado, contorno `seam-strong`, canto de 2px. Variante mono para qualquer valor de arquivo. O seletor nativo perde a caixa do navegador e ganha uma divisa de 1px em `ink-3`.
- **Focus:** contorno e borda em `override`. Filtro ativo mantém a borda azul em repouso.
- **Error / Disabled:** erro de validação pinta a borda de `fault` e mostra o problema em 12px vermelho abaixo. Travado pelo ambiente fica tracejado, transparente e com texto `ink-2`, acompanhado do cadeado.

### Controles de escolha
- **Interruptor:** pílula de 28×16px; ligado é fundo `override-dim`, contorno e botão `override`.
- **Seletor segmentado:** opções de 20px em `ink-3` dentro de um contorno comum; a ativa ganha `panel-raised`, tinta `ink` e um sublinhado interno de 1px `ink-2`. Travado vira tracejado.
- **Controle contínuo:** trilho de 2px preenchido até o valor na cor do que ele controla (azul para probabilidade, âmbar para latência, `ink-3` quando o override está desligado), polegar de tinta e campo numérico mono alinhado à direita com unidade em `ink-3`.

### Navigation
Não há navegação de páginas nem sidebar. A barra superior de 30px leva o nome em mono 600, itens separados por costura vertical com rótulos em versalete e pontos de estado, e um espaçador empurra histórico e aprendizado para a direita. Desconectada, a barra inteira recebe o véu `fault-dim`. As abas do painel de controles são rótulos em versalete: ativa em `ink` com traço de 1px colado à costura do cabeçalho.

### Mapa de topologia (assinatura)
Três colunas, cliente → rotas → upstreams, sobre um campo com grade de pontos de 1px a cada 12px na cor da costura. Nós são caixas `panel-raised` de canto 2px; a rota com override tem contorno azul, a selecionada ganha também fundo `override-dim`, o upstream fora tem contorno vermelho. Um medidor de 2px na base do nó mostra a probabilidade aplicada em âmbar ou vermelho. Arestas são curvas de 1px em `seam-strong`, azuis no caminho selecionado, vermelhas tracejadas para upstream fora; o resto recua por tom.

### Tráfego com waterfall (assinatura)
Tabela de linhas de 26px com cabeçalho colante em versalete e costura entre linhas. Hover em `panel-raised`, linha aberta em `override-dim` com traços azuis acima e abaixo. Cada linha termina num waterfall de 8px: upstream em `time-upstream`, injetado em âmbar, gateway em `time-gateway`. No detalhe, o waterfall vira faixas de 12px sobre trilho `ground` com eixo e marcas mono de 11px.

### Documento ao vivo (assinatura)
YAML em mono 12px com gutter numerado separado por costura. Chaves em `ink-2`, valores em `ink`, pontuação e comentários em `ink-3`; nenhuma cor de estado no realce. A linha que o controle ao lado acabou de gravar ganha um traço azul de 2px na margem e um brilho `override-dim` que se apaga em 1.6s. Com foco, a costura do gutter fica azul.

## Do's and Don'ts

### Do:
- **Do** separar regiões com costura de 1px (`seam`) ou degrau de tom; o console inteiro é `gap: 1px` sobre `seam`.
- **Do** reservar azul para seleção e override, âmbar para tempo injetado, vermelho para queda, sintetizado e upstream fora, e verde para o ponto saudável.
- **Do** pôr todo valor de arquivo ou requisição em JetBrains Mono 12px com algarismos tabulares e zero cortado.
- **Do** rotular painéis, colunas e grupos em versalete de 15px com espaçamento de 0.08 a 0.1em, em `ink-2` ou `ink-3`.
- **Do** manter controles entre 20 e 26px de altura e cantos em 2px.
- **Do** usar traço tracejado para travado, derivado ou incompleto, e dar forma além de cor a todo estado que o estreito precisar distinguir.
- **Do** limitar a animação à chegada de troca e à linha alterada do documento (1.6s, `cubic-bezier(0.16, 1, 0.3, 1)`), com transições de 120 a 160ms, e desligar tudo sob `prefers-reduced-motion`.
- **Do** desenhar ícones como SVG de 10px com traço de 1.5px em `currentColor`.

### Don't:
- **Don't** usar sombra projetada ou desfocada; só traço interno de 1px.
- **Don't** colorir botões, títulos ou falhas de leitura do próprio painel com cor de estado.
- **Don't** pintar de vermelho erro do upstream ou do gateway; o vermelho é do que o gateway fez com o tráfego.
- **Don't** recuar elementos abaixo de `ink-3` nem com opacidade sobre texto legível.
- **Don't** introduzir cards de métrica, gráficos decorativos ou sidebar de ícones.
- **Don't** carregar fontes ou recursos pela rede; tudo vai embutido no binário.
