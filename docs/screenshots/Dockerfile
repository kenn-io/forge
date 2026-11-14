# Every tool scripts/generate-docs-screenshots.mjs needs, for hosts that have
# only Docker. Versions follow go.mod, mise.toml and frontend/package.json.
# Trixie's Git 2.47 has for-each-ref --exclude, which the repository browser uses.
FROM golang:1.27.4-trixie
RUN apt-get update && apt-get install -y --no-install-recommends \
    poppler-utils \
    tmux \
    unzip \
    xz-utils \
    && rm -rf /var/lib/apt/lists/*
RUN case "$(dpkg --print-architecture)" in arm64) NODE_ARCH=arm64; BUN_ARCH=aarch64 ;; *) NODE_ARCH=x64; BUN_ARCH=x64 ;; esac && \
    curl -fsSL "https://nodejs.org/dist/v26.13.0/node-v26.13.0-linux-${NODE_ARCH}.tar.xz" \
      | tar -xJ -C /usr/local --strip-components=1 && \
    curl -fsSL "https://github.com/oven-sh/bun/releases/download/bun-v1.4.4/bun-linux-${BUN_ARCH}.zip" -o /tmp/bun.zip && \
    unzip -q /tmp/bun.zip -d /tmp && mv "/tmp/bun-linux-${BUN_ARCH}/bun" /usr/local/bin/bun && rm -rf /tmp/bun*
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
RUN npx -y playwright@1.64.1 install --with-deps chromium && chmod -R a+rX /ms-playwright
