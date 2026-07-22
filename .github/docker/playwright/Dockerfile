# syntax=docker/dockerfile:1

ARG PLAYWRIGHT_BASE_IMAGE=mcr.microsoft.com/playwright:v1.61.1-noble@sha256:5b8f294aff9041b7191c34a4bab3ac270157a28774d4b0660e9743297b697e48
FROM ${PLAYWRIGHT_BASE_IMAGE}

LABEL org.opencontainers.image.source="https://github.com/kenn-io/middleman"

ENV DEBIAN_FRONTEND=noninteractive

RUN sed -i \
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
      install -y --no-install-recommends tmux \
    && rm -rf /var/lib/apt/lists/*
