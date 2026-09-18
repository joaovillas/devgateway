# syntax=docker/dockerfile:1

# Imagem do gateway em três estágios: o painel é construído com Node, embutido
# no binário Go estático (CGO_ENABLED=0) e só o binário vai para a imagem
# final, sem shell nem runtime. Ver README.md, "Docker".

ARG NODE_VERSION=24
ARG GO_VERSION=1.26

# 1. Painel web: gera web/dist.
FROM node:${NODE_VERSION}-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. Binário: embute o web/dist do estágio anterior.
FROM golang:${GO_VERSION} AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/gamerjp64/gateway/internal/admin.Version=${VERSION}" \
      -o /out/gateway ./cmd/gateway \
 && mkdir -p /out/data/routes

# 3. Imagem final mínima: só o binário, rodando como usuário sem privilégio.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /usr/local/bin/gateway
# /data guarda gateway.json, routes/ e o histórico persistente; a API e o
# aprendizado escrevem nele, por isso pertence ao usuário nonroot (65532).
COPY --from=build --chown=65532:65532 /out/data /data
WORKDIR /data
ENV GATEWAY_CONFIG=/data/gateway.json
EXPOSE 8080 8081
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/gateway"]
