# syntax=docker/dockerfile:1

# The devgateway image in three stages: the panel is built with Node, embedded
# into the static Go binary (CGO_ENABLED=0), and only the binary lands in the
# final image, with no shell and no runtime. See README.md, "Docker".

ARG NODE_VERSION=24
ARG GO_VERSION=1.26

# 1. Web panel: produces web/dist.
FROM node:${NODE_VERSION}-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. Binary: embeds the web/dist from the previous stage.
FROM golang:${GO_VERSION} AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/joaovillas/devgateway/internal/admin.Version=${VERSION}" \
      -o /out/devgateway ./cmd/devgateway \
 && mkdir -p /out/data/routes

# 3. Minimal final image: just the binary, running as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/devgateway /usr/local/bin/devgateway
# /data holds gateway.json, routes/ and the persistent history; the API and
# learning write to it, so it belongs to the nonroot user (65532).
COPY --from=build --chown=65532:65532 /out/data /data
WORKDIR /data
ENV GATEWAY_CONFIG=/data/gateway.json
EXPOSE 8080 8081
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/devgateway"]
