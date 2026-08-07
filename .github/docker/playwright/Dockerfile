# syntax=docker/dockerfile:1

ARG BUN_IMAGE=oven/bun:1.3.14@sha256:e10577f0db68676a7024391c6e5cb4b879ebd17188ab750cf10024a6d700e5c4
ARG PLAYWRIGHT_BASE_IMAGE=mcr.microsoft.com/playwright:v1.61.1-noble@sha256:5b8f294aff9041b7191c34a4bab3ac270157a28774d4b0660e9743297b697e48
ARG VITE_PLUS_VERSION=0.2.3

FROM ${BUN_IMAGE} AS bun
FROM ${PLAYWRIGHT_BASE_IMAGE}

ARG VITE_PLUS_VERSION

LABEL org.opencontainers.image.source="https://github.com/kenn-io/middleman"

ENV BUN_INSTALL=/usr/local \
    DEBIAN_FRONTEND=noninteractive

COPY --from=bun /usr/local/bin/bun /usr/local/bin/bun

RUN node -e 'if (Number(process.versions.node.split(".")[0]) < 24) process.exit(1)' \
    && bun --version \
    && bun add --global --ignore-scripts "vite-plus@${VITE_PLUS_VERSION}" \
    && vp --version \
    && sed -i \
      -e 's|http://archive.ubuntu.com/ubuntu|https://mirrors.edge.kernel.org/ubuntu|g' \
      -e 's|http://security.ubuntu.com/ubuntu|https://mirrors.edge.kernel.org/ubuntu|g' \
      /etc/apt/sources.list.d/ubuntu.sources \
    && apt-get \
      -o Acquire::Retries=5 \
      -o Acquire::ForceIPv4=true \
      -o Acquire::http::Timeout=30 \
      -o Acquire::https::Timeout=30 \
      update \
    && apt-get \
      -o Acquire::Retries=5 \
      -o Acquire::ForceIPv4=true \
      -o Acquire::http::Timeout=30 \
      -o Acquire::https::Timeout=30 \
      install -y --no-install-recommends poppler-utils tmux \
    && rm -rf /var/lib/apt/lists/*
