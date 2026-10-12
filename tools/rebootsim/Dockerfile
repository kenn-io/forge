FROM debian:trixie-slim
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates curl git procps sqlite3 tmux \
  && rm -rf /var/lib/apt/lists/*
COPY kenn-forge rebootsim fake-agent /usr/local/bin/
ENV SHELL=/bin/bash
CMD ["sleep", "infinity"]
