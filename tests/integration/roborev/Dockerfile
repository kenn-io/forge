FROM golang:1.26 AS builder
ARG ROBOREV_REF=main
COPY . /src
WORKDIR /src
RUN git checkout ${ROBOREV_REF} && \
    go build -o /roborev ./cmd/roborev/

FROM debian:bookworm-slim
RUN apt-get update && \
    apt-get install -y --no-install-recommends curl socat && \
    rm -rf /var/lib/apt/lists/*
COPY --from=builder /roborev /usr/local/bin/roborev
RUN mkdir -p /data
ENV ROBOREV_DATA_DIR=/data

# The daemon enforces loopback-only bind addresses.
# Docker port mapping requires 0.0.0.0 inside the container.
# Workaround: bind daemon to 127.0.0.1:7374 and use socat
# to relay 0.0.0.0:7373 -> 127.0.0.1:7374.
EXPOSE 7373
ENTRYPOINT ["/bin/sh", "-c", "\
  socat TCP-LISTEN:7373,fork,reuseaddr,bind=0.0.0.0 TCP:127.0.0.1:7374 & \
  exec roborev \"$@\"" , "--"]
