# GitLab CE Container Fixture

This fixture is an opt-in compatibility check for the GitLab provider. It is intentionally outside the default test suite because GitLab CE is slow to pull and boot.

Run the Go e2e test:

```sh
MIDDLEMAN_GITLAB_CONTAINER_E2E=1 make test-gitlab-container
```

The test starts `gitlab/gitlab-ce:19.1.1-ce.0` through testcontainers' Docker Compose module, waits for GitLab Rails to serve the sign-in page, runs `bootstrap.sh`, and syncs the seeded project into a real SQLite database. Override the image with `MIDDLEMAN_GITLAB_IMAGE` when checking a different GitLab release.

By default the Go test uses the Compose project `middleman-gitlab-e2e`, maps GitLab to a free loopback port, and runs `docker compose down` without `-v` during cleanup. The named GitLab volumes are kept so repeated local runs do not pay the full database initialization cost. Set `MIDDLEMAN_GITLAB_COMPOSE_PROJECT` or `GITLAB_HTTP_PORT` if you need a specific project name or port.

The test expects a working Docker runtime with Ryuk enabled. CI jobs that run this target must provide Docker access to the test process. Only set `TESTCONTAINERS_HOST_OVERRIDE` when the Go test itself runs inside another container and mapped ports are reachable through a non-default host.

For manual debugging with Docker Compose:

```sh
docker compose -f scripts/e2e/gitlab/docker-compose.yml up -d
GITLAB_URL=http://127.0.0.1:${GITLAB_HTTP_PORT:-18080} scripts/e2e/gitlab/wait.sh
GITLAB_URL=http://127.0.0.1:${GITLAB_HTTP_PORT:-18080} \
  GITLAB_ROOT_PASSWORD=${GITLAB_ROOT_PASSWORD:-V9q!T3m#R7p-L2x@N6s} \
  scripts/e2e/gitlab/bootstrap.sh /tmp/middleman-gitlab-manifest.json
docker compose -f scripts/e2e/gitlab/docker-compose.yml down
```

Use `docker compose -f scripts/e2e/gitlab/docker-compose.yml down -v` only when you intentionally want to discard the GitLab database/config volumes and force a cold initialization.

With Colima, prefer the Docker context first:

```sh
docker context use colima
```

If your shell or CI wrapper is not context-aware, export the socket explicitly so testcontainers can connect and Ryuk can mount the in-VM Docker socket:

```sh
export DOCKER_HOST="unix://$HOME/.colima/default/docker.sock"
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
MIDDLEMAN_GITLAB_CONTAINER_E2E=1 make test-gitlab-container
```

Set `MIDDLEMAN_KEEP_GITLAB_FIXTURE=1` only when you want to leave the test's Compose stack running after the Go test exits.

The bootstrap manifest contains the dynamically assigned project id, MR iid, issue iid, token, provider host, nested repo path, and release tag. Tests consume the manifest instead of hard-coding GitLab-assigned ids.

## Baked Fixture Image

Cold GitLab boot plus API bootstrap is slow. To prepare a reusable local image from the same bootstrap recipe:

```sh
make gitlab-fixture-bake
MIDDLEMAN_GITLAB_IMAGE=middleman/gitlab-ce-fixture:19.1.1-ce.0 \
  MIDDLEMAN_GITLAB_CONTAINER_E2E=1 \
  make test-gitlab-container
```

The bake flow uses `docker-bake.hcl` to build a small runtime layer, starts it once, runs `bootstrap.sh`, exports GitLab's official volume paths into tarballs under `scripts/e2e/gitlab/fixture-data/`, and then builds `middleman/gitlab-ce-fixture:<tag>` with those tarballs. The final image restores the tars into fresh runtime volumes before GitLab starts.

The official GitLab CE image declares `/etc/gitlab`, `/var/log/gitlab`, and `/var/opt/gitlab` as volumes, so plain `docker commit` does not reliably capture seeded state. Keep `bootstrap.sh` as the canonical fixture recipe and use the baked image only as a startup-time cache.
