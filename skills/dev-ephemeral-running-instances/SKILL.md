---
name: dev-ephemeral-running-instances
description: Use only when the user explicitly invokes dev-ephemeral-running-instances. Lists middleman dev-ephemeral status files across git worktrees, checks recorded launcher/backend/frontend PIDs and backend/frontend URLs, and reports which worktree each running, degraded, stale, or invalid instance belongs to.
---

# Dev Ephemeral Running Instances

## Overview

List middleman `dev-ephemeral` instances across Codex git worktrees without stopping or cleaning up any process. Use the bundled Python script as the source of truth for process and URL checks.

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

## Safety

Do not kill, stop, delete, terminate, or clean up any process, tmux session, service, daemon, server, job, status file, or worktree while using this skill. Only inspect and report.
