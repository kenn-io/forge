FROM golang:1.27.0 AS builder
ARG ROBOREV_REF=main
COPY . /src
WORKDIR /src
RUN git checkout ${ROBOREV_REF}
RUN for attempt in 1 2 3; do \
      go mod download && break; \
      [ "$attempt" -lt 3 ] || exit 1; \
      sleep 5; \
    done
RUN GOPROXY=off go build -o /roborev ./cmd/roborev/
COPY --from=forge tests/integration/roborev/tcp-forward.go /tcp-forward.go
RUN go build -o /tcp-forward /tcp-forward.go

FROM golang:1.27.0
RUN command -v git && command -v curl
COPY --from=builder /roborev /usr/local/bin/roborev
COPY --from=builder /tcp-forward /usr/local/bin/tcp-forward
RUN mkdir -p /data
ENV ROBOREV_DATA_DIR=/data

# The daemon enforces loopback-only bind addresses.
# Docker port mapping requires 0.0.0.0 inside the container.
# Workaround: bind daemon to 127.0.0.1:7374 and use a small Go proxy
# to relay 0.0.0.0:7373 -> 127.0.0.1:7374.
EXPOSE 7373
ENTRYPOINT ["/bin/sh", "-c", "\
  tcp-forward & \
  exec roborev \"$@\"" , "--"]
