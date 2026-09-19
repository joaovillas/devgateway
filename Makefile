BIN     ?= bin/devgateway
GOFLAGS ?=
export CGO_ENABLED ?= 0

# Version stamped into the binary (shown by GET /api/status).
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X github.com/gamerjp64/devgateway/internal/admin.Version=$(VERSION)
PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
DIST      ?= dist
IMAGE     ?= devgateway:$(VERSION)

.PHONY: all build web web-clean test race lint vet fmt-check clean release release-bins docker

all: lint test build

## build: build the binary with whatever frontend is in web/dist
build:
	go build $(GOFLAGS) -o $(BIN) ./cmd/devgateway

## web: build the panel into web/dist (replaces the placeholder; see web/README.md)
web:
	cd web && npm ci && npm run build

## web-clean: delete the panel build and restore the committed placeholder
web-clean:
	cd web && npm run clean

## release: build the panel and one static binary per platform into dist/
release: web
	$(MAKE) release-bins

## release-bins: only the binaries, using the web/dist already built
release-bins:
	rm -rf $(DIST)
	mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; \
		if [ "$$os" = windows ]; then ext=.exe; fi; \
		out=$(DIST)/devgateway_$(VERSION)_$${os}_$${arch}$$ext; \
		echo "  $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/devgateway; \
	done

## docker: build the image (the Dockerfile builds the panel and the binary)
docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

## test: run the tests without the race detector
test:
	go test ./...

## race: run the tests with -race (needs CGO and a C compiler)
race:
	CGO_ENABLED=1 go test -race ./...

## lint: formatting and go vet
lint: fmt-check vet

vet:
	go vet ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "files that need gofmt:"; echo "$$out"; exit 1; fi

clean:
	rm -rf bin $(DIST)
