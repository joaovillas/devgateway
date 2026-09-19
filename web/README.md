# Web panel

The gateway's control panel: Vite, React and TypeScript. The build goes to `web/dist`, which the Go binary embeds with `//go:embed all:web/dist` and serves on the admin port. The panel only talks to the API documented in [`docs/api.md`](../docs/api.md).

## Commands

```sh
cd web
npm ci              # exact dependencies from package-lock.json
npm run dev         # vite on :5173, with /api forwarded to http://localhost:8081
npm run typecheck   # tsc without emitting
npm run build       # produces web/dist
npm run clean       # deletes the build and restores the versioned placeholder
```

From the repository root, `make web` runs `npm ci` and `npm run build`. After it, `make build` produces a binary with the panel embedded.

To point `npm run dev` at another admin port, use `GATEWAY_ADMIN_URL=http://localhost:18081 npm run dev`.

## The `web/dist/index.html` placeholder

The versioned `web/dist/index.html` is a placeholder. It exists so that `go build` works in a clean clone, without Node (see `design.md`, "Frontend embedded via `go:embed`"). The `.gitignore` ignores everything else in `web/dist`.

The Vite build writes straight into `web/dist` and **overwrites the placeholder** with the real `index.html`. It is the simplest solution that stays correct. Go embeds the whole directory at once, and the `index.html` that gets served has to be the one referencing that build's hashed assets. Building into another directory and copying would only move the problem, because the file copied to `web/dist/index.html` would show up as modified in git all the same.

The consequence is that, after a build, `git status` shows `web/dist/index.html` as modified. Do not commit it: it points at assets that are not versioned. To go back to the clean-clone state:

```sh
npm run clean                            # or, just the placeholder:
git checkout -- web/dist/index.html
```

## Fonts

The fonts come from the packages `@fontsource-variable/source-sans-3` (interface) and `@fontsource-variable/jetbrains-mono` (data, documents and tabular figures). Vite copies the `.woff2` files to `web/dist/assets`, so the binary serves everything without network access. No resource is loaded from a CDN.

## Structure

- `src/api/`: typed API client. `types.ts` mirrors the Go types, `client.ts` has one function per operation in `docs/api.md` and `events.ts` handles the SSE, with reconnection and connection state.
- `src/components/`: the console's fixed panels (bar, map, traffic, route, document).
- `src/styles/tokens.css`: the tokens of the visual world, defined in `.impeccable/surfaces/web.md`.
