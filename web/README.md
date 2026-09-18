# Painel web

Painel de controle do gateway: Vite, React e TypeScript. O build vai para `web/dist`, que o binário Go embute com `//go:embed all:web/dist` e serve na porta de administração. O painel só fala com a API documentada em [`docs/api.md`](../docs/api.md).

## Comandos

```sh
cd web
npm ci              # dependências exatas do package-lock.json
npm run dev         # vite em :5173, com /api encaminhado para http://localhost:8081
npm run typecheck   # tsc sem emitir
npm run build       # gera web/dist
npm run clean       # apaga o build e restaura o placeholder versionado
```

Da raiz do repositório, `make web` roda `npm ci` e `npm run build`. Depois dele, `make build` produz um binário com o painel embutido.

Para apontar o `npm run dev` para outra porta de administração, use `GATEWAY_ADMIN_URL=http://localhost:18081 npm run dev`.

## O placeholder de `web/dist/index.html`

O `web/dist/index.html` versionado é um placeholder. Ele existe para que `go build` funcione num clone limpo, sem Node (ver `design.md`, "Frontend embutido via go:embed"). O `.gitignore` ignora todo o resto de `web/dist`.

O build do Vite escreve direto em `web/dist` e **sobrescreve o placeholder** com o `index.html` real. É a solução mais simples que continua correta. O Go embute o diretório inteiro de uma vez, e o `index.html` servido precisa ser o que referencia os assets com hash daquele build. Montar o build em outro diretório e copiar só mudaria o lugar do problema, porque o arquivo copiado para `web/dist/index.html` apareceria modificado no git do mesmo jeito.

A consequência é que, depois de um build, o `git status` mostra `web/dist/index.html` modificado. Não faça commit dele: ele aponta para assets que não estão versionados. Para voltar ao estado do clone limpo:

```sh
npm run clean                            # ou, só o placeholder:
git checkout -- web/dist/index.html
```

## Fontes

As fontes vêm dos pacotes `@fontsource-variable/source-sans-3` (interface) e `@fontsource-variable/jetbrains-mono` (dados, documentos e figuras tabulares). O Vite copia os `.woff2` para `web/dist/assets`, então o binário serve tudo sem acesso à rede. Nenhum recurso é carregado de CDN.

## Estrutura

- `src/api/`: cliente tipado da API. `types.ts` espelha os tipos Go, `client.ts` tem uma função por operação de `docs/api.md` e `events.ts` cuida do SSE, com reconexão e estado de conexão.
- `src/components/`: os painéis fixos do console (barra, mapa, tráfego, rota, documento).
- `src/styles/tokens.css`: os tokens do mundo visual, definidos em `.impeccable/surfaces/web.md`.
