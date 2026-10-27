# syntax=docker/dockerfile:1

# ---- build stage ----------------------------------------------------------
# Dependencies are vendored, so this stage needs no network access.
FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG TARGETOS
ARG TARGETARCH

# CGO is not needed: pure-Go TLS plus the gVisor netstack produce a fully
# static binary that runs on distroless.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
      go build -mod=vendor -trimpath \
      -ldflags "-s -w \
        -X github.com/your-github-user/tjsocks/internal/version.Version=${VERSION} \
        -X github.com/your-github-user/tjsocks/internal/version.Commit=${COMMIT} \
        -X github.com/your-github-user/tjsocks/internal/version.Date=${DATE}" \
      -o /out/tjsocks ./cmd/tjsocks

# ---- runtime stage --------------------------------------------------------
# distroless/static provides CA certificates (required for the VPN's TLS
# certificate) and runs as an unprivileged user in ~2 MB.
FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev

LABEL org.opencontainers.image.title="tjsocks" \
      org.opencontainers.image.description="Tongji campus VPN to SOCKS5 gateway (TCP + UDP)" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.source="https://github.com/your-github-user/tjsocks"

COPY --from=build /out/tjsocks /usr/local/bin/tjsocks

EXPOSE 1080/tcp
EXPOSE 1080/udp

USER nonroot:nonroot

# The binary probes itself, so no shell or extra tooling is needed.
# Override the address with `--health-cmd` when SOCKS_LISTEN is not :1080.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
    CMD ["/usr/local/bin/tjsocks", "-healthcheck", "127.0.0.1:1080"]

ENTRYPOINT ["/usr/local/bin/tjsocks"]
