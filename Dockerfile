# syntax=docker/dockerfile:1
# Base images are pinned by digest; Dependabot keeps them current.
FROM golang:1.27.4-alpine@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /garmin-mcp ./cmd/garmin-mcp

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
# Proves ownership of the image to the official MCP Registry.
LABEL io.modelcontextprotocol.server.name="io.github.sasha-sup/garmin-mcp"
LABEL org.opencontainers.image.source="https://github.com/sasha-sup/garmin-mcp" \
      org.opencontainers.image.description="Unofficial read-only MCP server for your own Garmin Connect data" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /garmin-mcp /garmin-mcp
# The container is started per session (docker run -i --rm), so the cache only
# lives for that session; mount a volume here to keep it.
ENV GARMIN_CACHE_DIR=/tmp/garmin-mcp
ENTRYPOINT ["/garmin-mcp"]
