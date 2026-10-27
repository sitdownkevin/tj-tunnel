# syntax=docker/dockerfile:1

# ---- dashboard bundle -----------------------------------------------------
# The React/MUI UI is compiled here and embedded into the Go binary, so the
# runtime image never needs Node.
FROM node:22-alpine AS web

WORKDIR /web

# Install dependencies first so the layer is cached independently of sources.
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund

COPY web/ ./
RUN npm run build

# ---- Go build -------------------------------------------------------------
# Dependencies are vendored, so this stage needs no network access.
FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Drop in the freshly built dashboard before compiling.
COPY --from=web /internal/web/dist ./internal/web/dist

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
      go build -mod=vendor -trimpath \
      -ldflags "-s -w \
        -X github.com/sitdownkevin/tj-tunnel/internal/version.Version=${VERSION} \
        -X github.com/sitdownkevin/tj-tunnel/internal/version.Commit=${COMMIT} \
        -X github.com/sitdownkevin/tj-tunnel/internal/version.Date=${DATE}" \
      -o /out/tj-tunnel ./cmd/tj-tunnel

# ---- runtime --------------------------------------------------------------
# distroless/static provides CA certificates (the VPN gateway uses TLS) and
# runs as an unprivileged user in about 2 MB.
FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev

LABEL org.opencontainers.image.title="TJ Tunnel" \
      org.opencontainers.image.description="Campus VPN to SOCKS5 gateway (TCP + UDP) with a live dashboard" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.source="https://github.com/sitdownkevin/tj-tunnel"

COPY --from=build /out/tj-tunnel /usr/local/bin/tj-tunnel

EXPOSE 1080/tcp
EXPOSE 1080/udp
EXPOSE 8080/tcp

USER nonroot:nonroot

# The binary probes itself, so no shell or extra tooling is needed.
# Override the address with `--health-cmd` when SOCKS_LISTEN is not :1080.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD ["/usr/local/bin/tj-tunnel", "-healthcheck", "127.0.0.1:1080"]

ENTRYPOINT ["/usr/local/bin/tj-tunnel"]
