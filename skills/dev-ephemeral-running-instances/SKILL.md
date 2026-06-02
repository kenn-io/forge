---
name: dev-ephemeral-running-instances
description: Use only when the user explicitly invokes dev-ephemeral-running-instances. Must be run outside the Codex sandbox because sandboxed scans cannot reliably see all Codex worktrees, uv/Python state, or the process table. Lists middleman dev-ephemeral status files across git worktrees, checks recorded launcher/backend/frontend PIDs and backend/frontend URLs, reports which worktree each running, degraded, stale, or invalid instance belongs to, and prepares clean shutdown command lists when explicitly requested.
---

# Dev Ephemeral Running Instances

## Overview

List middleman `dev-ephemeral` instances across Codex git worktrees without stopping or cleaning up any process by default. Use the bundled Python script as the source of truth for process and URL checks.

## Sandbox Requirement

Run this skill outside the Codex sandbox. The inspector needs read access to all `~/.codex/worktrees` entries, `uv`/Python runtime state, the process table, and loopback URLs. Sandboxed runs can falsely report no status files, fail to run `ps`, or fail to initialize `uv`; treat those results as invalid and rerun outside the sandbox.

## Workflow

1. Run the inspector from the skill directory or pass its absolute path:

   ```sh
   uv run --script skills/dev-ephemeral-running-instances/scripts/list_running_instances.py
   ```

2. If working outside a middleman checkout, pass the worktrees root explicitly:

   ```sh
   uv run --script /path/to/dev-ephemeral-running-instances/scripts/list_running_instances.py \
     --worktrees-root "$HOME/.codex/worktrees"
   ```

3. Report the table grouped by status. Include each worktree, run directory, branch, backend/frontend ports or URLs, and the reason for degraded/stale status when present.

## Clean Shutdown Preparation

When the user asks to shut down instances, prepare a target list and command list first. Do not execute the stop commands until the user explicitly approves the exact targets and intended command.

Use the project launcher stop path rather than killing PIDs manually:

```sh
go run ./tools/devephemeral -stop -status /absolute/path/to/dev-ephemeral.json
```

To generate a shutdown plan while keeping one worktree, run:

```sh
uv run --script skills/dev-ephemeral-running-instances/scripts/list_running_instances.py \
  --exclude-worktree /Users/mariusvniekerk/.codex/worktrees/e007/middleman \
  --emit-stop-commands
```

Present the generated target list and commands to the user. After approval, run the commands from a middleman checkout. The `devephemeral` stop path verifies process identity from status-file start times, interrupts process groups first, escalates to terminate after the built-in grace period, and removes the status file only when shutdown succeeds or no matching live processes remain.

## Script Behavior

- Discovers `dev-ephemeral.json` files under `~/.codex/worktrees` by default.
- Resolves the owning git worktree by walking upward to the nearest `.git` directory or file.
- Reads the branch with `git -C <worktree> branch --show-current`; detached checkouts are reported as `detached HEAD`.
- Checks each recorded `pid`, `backend_pid`, and `frontend_pid` with `os.kill(pid, 0)`.
- Probes recorded `backend_url` and `frontend_url` with a short HTTP timeout.
- Classifies rows:
  - `live`: launcher/backend/frontend PIDs are alive and both URLs respond with HTTP 2xx or 3xx.
  - `degraded`: at least one PID or URL check is alive/responding, but the full stack is not healthy.
  - `stale`: no recorded PID is alive and neither URL responds.
  - `invalid`: the status file cannot be parsed or does not contain the expected shape.
- With `--emit-stop-commands`, prints clean `go run ./tools/devephemeral -stop -status ...` commands for matching, non-invalid rows. Use `--exclude-worktree` one or more times to keep specific worktrees running.

## Safety

Do not kill, stop, delete, terminate, or clean up any process, tmux session, service, daemon, server, job, status file, or worktree unless the user has explicitly approved the exact target list and intended command. When approval is missing, only inspect and report.
