# Contributing

Thanks for taking the time. devgateway is a single Go binary with an embedded
React panel, so the whole project builds from a clone with two tools installed.

## What you need

- **Go 1.26** — everything under `cmd/devgateway` and `internal/`.
- **Node 24** — only to build the panel in `web/`. Without it, `go build
  ./cmd/devgateway` still works: the binary ships a placeholder page instead of
  the panel, and the proxy and the API behave normally.
- A **C compiler** if you want to run the tests with the race detector locally
  (see below).

## Running it

```sh
make build         # bin/devgateway with whatever is in web/dist
make test          # go test ./...
make race          # go test -race ./... (needs CGO and a C compiler)
make lint          # gofmt check and go vet
make web           # builds the panel into web/dist
make web-clean     # restores web/dist to the versioned placeholder
```

`web/dist/index.html` is committed as a placeholder so that `go build` works in
a clone without Node. After `make web`, do not commit the generated `web/dist`;
`make web-clean` puts the placeholder back.

If your machine cannot build cgo (Windows without a C toolchain, for example),
run the race detector in a container instead:

```sh
docker run --rm -v "$PWD:/src" -v gomodcache:/go/pkg/mod -w /src \
  golang:1.26 go test -race -count=1 ./...
```

In Git Bash on Windows, prefix that with `MSYS_NO_PATHCONV=1` and pass
`"$(cygpath -w $PWD):/src"`, since Git Bash rewrites paths handed to native
programs.

To try a change end to end, `examples/run.sh` brings up two toy services with
ready-made routes, and `examples/e2e.sh` checks the whole thing.

## Specs come first

The specs live in `openspec/`: `openspec/changes/<change>/` holds a change being
worked on — proposal, design, tasks and the spec deltas it adds or modifies —
and `openspec/specs/` holds the accumulated behavior once a change is archived.

This project is spec-driven: a spec describes observable behavior as
requirements with `SHALL`/`MUST` and one or more `Scenario` blocks, and the code
exists to satisfy them. **Any change in behavior starts in the spec** — you
write or amend the requirement and its scenarios first, then implement it, and
the tests mirror those scenarios.

Bug fixes that restore behavior a spec already describes, refactors, docs and
build changes do not need a spec change. If the bug exists because the spec was
silent or wrong, fix the spec in the same pull request.

## Style

- Code, comments, commit messages, error messages, docs and specs are written in
  **English**.
- Error messages stay precise: name the file, the field, the line and the cause,
  the way the config parser already does. An error that only says something
  failed is a bug.
- Tests follow the scenarios of the spec they cover, one test per scenario, with
  a name that says what the scenario asserts. Prefer table-driven tests when the
  scenarios differ only in input.
- Keep `gofmt` clean; `make lint` fails otherwise.
- No new runtime dependency without a reason stated in the pull request. The
  binary is meant to run with nothing else installed.

## Proposing a change

Open an issue first for anything that changes behavior, adds configuration or
touches the wire format of the API. Say what you are trying to do, not only what
you want built — the routing, override and learning models are opinionated and
there is often an existing way in.

Small fixes (a typo, a broken link, a clearly wrong branch) can go straight to a
pull request.

## Opening a pull request

1. Branch from `main`.
2. Make the spec change, when the behavior changes, in the same branch.
3. Run `make lint`, `make test` and `make race` before pushing.
4. Write the commit message in English: a short imperative subject line (about
   50 characters, no trailing period), a blank line, then a body explaining why
   the change exists and anything a reviewer would otherwise have to guess. One
   logical change per commit.
5. In the pull request description, link the issue or the change under
   `openspec/changes/`, and say how you verified it — the commands you ran, or
   the requests you sent through the gateway.
6. CI runs `make build`, `make lint`, `make race` and a `CGO_ENABLED=0` build on
   Linux. **It has to be green.** If the change touches file handling or process
   control, say which platforms you tried it on: CI does not cover Windows or
   macOS.

Review is about the spec as much as the code, so expect questions about which
requirement a change satisfies.
