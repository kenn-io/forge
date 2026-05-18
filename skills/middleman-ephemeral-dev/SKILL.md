---
name: middleman-ephemeral-dev
description: Use when working on middleman local development sessions that need backend and frontend on ephemeral/free ports, isolated generated config, copied SQLite state, or dev-ephemeral status JSON/PIDs.
---

# Middleman Ephemeral Dev

## Core Rule

Use `make dev-ephemeral` when the user wants a local middleman dev stack without fixed ports or without mutating the normal config/database. Do not hand-roll separate `make dev` and `make frontend-dev` commands unless the user explicitly asks for manual process control.

## Workflow

1. Start the stack:

   ```sh
   make dev-ephemeral
   ```

2. The default work directory is `tmp/dev-ephemeral`. Re-running the command while that stack is live prints the existing status instead of starting a duplicate stack.

3. For a separate concurrent run directory or explicit ports, pass `ARGS`:

   ```sh
   make dev-ephemeral ARGS="-work-dir tmp/my-run"
   make dev-ephemeral ARGS="-backend-port 19091 -frontend-port 15174"
   ```

4. Use a fresh empty DB only when requested:

   ```sh
   make dev-ephemeral ARGS="-fresh-db"
   ```

## Behavior Contract

- The launcher starts both backend and frontend.
- It writes a generated config at `<work-dir>/config.toml`.
- It copies the configured source SQLite DB by default into `<work-dir>/data/middleman.db`.
- It passes `MIDDLEMAN_CONFIG=<work-dir>/config.toml` to both processes.
- It passes `MIDDLEMAN_API_URL=<backend-url>` to the frontend.
- It writes typed status JSON at `<work-dir>/dev-ephemeral.json`.
- `make dev-ephemeral-stop` without arguments stops the default `tmp/dev-ephemeral` stack.
- The launcher is intentionally unsupported on Windows for now.

## Status JSON

Read `<work-dir>/dev-ephemeral.json` when another tool or response needs process discovery:

```json
{
  "pid": 1001,
  "backend_pid": 1002,
  "frontend_pid": 1003,
  "backend_port": 19091,
  "frontend_port": 15174,
  "config_path": "tmp/my-run/config.toml",
  "data_dir": "tmp/my-run/data",
  "backend_url": "http://127.0.0.1:19091",
  "frontend_url": "http://127.0.0.1:15174"
}
```

Treat this as the source of truth for selected ports and PIDs instead of scraping terminal output.

## Verification

For changes to the workflow or this skill, run:

```sh
go test ./tools/devephemeral -shuffle=on
go run ./cmd/testify-helper-check ./tools/devephemeral
make help
```

Before claiming the stack is running, verify the status file exists and contains both child PIDs and both URLs.
