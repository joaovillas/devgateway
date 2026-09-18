BIN     ?= bin/gateway
GOFLAGS ?=
export CGO_ENABLED ?= 0

.PHONY: all build test race lint vet fmt-check clean

all: lint test build

## build: compila o binário com o frontend que estiver em web/dist
build:
	go build $(GOFLAGS) -o $(BIN) ./cmd/gateway

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
	rm -rf bin
