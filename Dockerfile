# syntax=docker/dockerfile:1
ARG BUN_IMAGE=oven/bun:1.4.0@sha256:5ff609364c049b54eb0ff560ec96319729a972078ef2c755d758f0c6ef89c2d6
FROM --platform=$BUILDPLATFORM ${BUN_IMAGE} AS bun
FROM --platform=$BUILDPLATFORM node:24.16.0-bookworm-slim AS frontend
WORKDIR /src
COPY --from=bun /usr/local/bin/bun /usr/local/bin/bun
ENV UV_THREADPOOL_SIZE=2 RAYON_NUM_THREADS=2
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
COPY . .
RUN bun install --frozen-lockfile \
    && node scripts/build-frontend.mjs \
    && cd packages/github-app-ui && node ../../node_modules/vite-plus/bin/vp build --logLevel warn

FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-p=2 GOMAXPROCS=2
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/frontend/dist ./internal/web/dist
COPY --from=frontend /src/packages/github-app-ui/dist ./internal/githubapp/ui/dist
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_DATE=unknown
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${REVISION} -X main.buildDate=${BUILD_DATE}" \
    -o /out/kenn-forge ./cmd/kenn-forge

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git openssh-client tmux \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 1000 forge \
    && useradd --uid 1000 --gid 1000 --create-home --shell /bin/sh forge
COPY --from=build /out/kenn-forge /usr/local/bin/kenn-forge
COPY LICENSE /usr/share/doc/kenn-forge/LICENSE
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.source="https://github.com/kenn-io/forge" \
    org.opencontainers.image.version=$VERSION \
    org.opencontainers.image.revision=$REVISION \
    org.opencontainers.image.created=$BUILD_DATE
ENV HOME=/home/forge \
    PATH=/home/forge/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    KENN_FORGE_HOST=0.0.0.0 \
    KENN_FORGE_PORT=8091 \
    KENN_FORGE_REQUIRE_AUTH=true
USER 1000:1000
WORKDIR /home/forge
EXPOSE 8091
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=6 \
    CMD curl --noproxy '*' --fail --silent --show-error \
    -H "X-Forwarded-Host: 127.0.0.1:${KENN_FORGE_PORT}" \
    "http://127.0.0.1:${KENN_FORGE_PORT}/healthz" || exit 1
ENTRYPOINT ["kenn-forge"]
CMD ["serve"]
