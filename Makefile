BIN     ?= bin/gateway
GOFLAGS ?=
export CGO_ENABLED ?= 0

# Versão gravada no binário (aparece em GET /api/status).
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X github.com/gamerjp64/gateway/internal/admin.Version=$(VERSION)
PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
DIST      ?= dist
IMAGE     ?= gateway:$(VERSION)

.PHONY: all build web web-clean test race lint vet fmt-check clean release release-bins docker

all: lint test build

## build: compila o binário com o frontend que estiver em web/dist
build:
	go build $(GOFLAGS) -o $(BIN) ./cmd/gateway

## web: constrói o painel em web/dist (substitui o placeholder; ver web/README.md)
web:
	cd web && npm ci && npm run build

## web-clean: apaga o build do painel e restaura o placeholder versionado
web-clean:
	cd web && npm run clean

## release: constrói o painel e gera um binário estático por plataforma em dist/
release: web
	$(MAKE) release-bins

## release-bins: só os binários, com o web/dist que já estiver construído
release-bins:
	rm -rf $(DIST)
	mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; \
		if [ "$$os" = windows ]; then ext=.exe; fi; \
		out=$(DIST)/gateway_$(VERSION)_$${os}_$${arch}$$ext; \
		echo "  $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/gateway; \
	done

## docker: constrói a imagem (o Dockerfile constrói o painel e o binário)
docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

## test: roda os testes sem detector de corrida
test:
	go test ./...

## race: roda os testes com -race (exige CGO e um compilador C)
race:
	CGO_ENABLED=1 go test -race ./...

## lint: formatação e go vet
lint: fmt-check vet

vet:
	go vet ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "arquivos sem gofmt:"; echo "$$out"; exit 1; fi

clean:
	rm -rf bin $(DIST)
