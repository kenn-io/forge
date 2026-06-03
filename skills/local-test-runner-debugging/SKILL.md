---
name: local-test-runner-debugging
description: Use when tests do not run locally, hang locally, fail to launch local runners, or appear blocked by host resource, process, tmux, Playwright, Vitest, Node, file descriptor, ulimit, stale artifact, or cleanup issues.
---

# Local Test Runner Debugging

## Core Rule

Prove whether the failure is local runner/environment pressure or a product/test defect before changing product code. Start with non-destructive inspection and minimal runner probes.

## Diagnosis Order

1. Reproduce the smallest command that shows the problem. Preserve repo test rules: direct `go test` gets `-shuffle=on`, do not add `-count=1`, and do not use `-v` unless the failure needs it.
2. Capture the failure shape: command, timeout/hang point, exit status, stderr, and whether the runner launched at all.
3. Check host pressure before editing code:
   - `ulimit -n`, `ulimit -u`, current shell limits, and obvious file descriptor or process exhaustion.
   - Process table pressure from stale `node`, `bun`, `vitest`, `playwright`, browser, `go test`, `tmux`, or local server processes.
   - Open ports, stuck watchers, leftover sockets, and temp/status files tied to previous runs.
4. Look for cleanup bypassed by `SIGKILL`, interrupted terminals, crashed runners, or timed-out harnesses. Treat stale artifacts as evidence first, not as something to delete immediately.
5. Run minimal probes before full suites:
   - runner version/help command;
   - one focused test file/package;
   - a no-op or list-only runner mode when available;
   - a fresh temp output directory when the runner supports it.

## Tmux Orphan Checks

middleman tests can spawn real tmux sessions. If a test process is `SIGKILL`ed, its normal cleanup can be bypassed and those sessions may remain.

Inspect candidates without deleting them:

```sh
tmux list-sessions -F '#{session_name}	#{session_created}	#{session_attached}	#{session_windows}'
tmux show-options -qv -t <session> @middleman_owner
tmux display-message -p -t <session> '#{pane_current_path} #{pane_current_command} #{pane_title}'
tmux capture-pane -p -t <session> -S -80
```

A tmux session is a plausible middleman test orphan only when the evidence lines up:

- the name matches middleman's managed shapes: `middleman-<16 lowercase hex>` or `middleman-<16 lowercase hex>-<16 lowercase hex>`;
- it is unattached and not being used by a live middleman dev server, workspace, or runtime session;
- its `@middleman_owner` marker points at this checkout's manager, or other evidence clearly ties it to the failed test run;
- its pane path, command, title, or captured output points at this repo/test run rather than a user shell;
- if the local SQLite state is available, it is not referenced by a live workspace row or stored runtime tmux-session row.

Do not classify a session as safe to delete from name alone. If any check is ambiguous, leave it out of the cleanup target list and report the uncertainty.

## Safety

Never kill, stop, delete, terminate, remove, or clean up processes, tmux sessions, servers, jobs, status files, browser state, sockets, or test artifacts unless the user explicitly approves the exact target list and intended command. You may inspect and report without approval.

If cleanup seems necessary, present:

- the exact resources found;
- why they appear tied to the failing test run;
- the exact command you propose;
- what will remain untouched.

Wait for approval before executing cleanup.

## Distinguish Failure Types

- **Local environment failure:** runner cannot start, hangs before test code executes, errors mention resource limits, file descriptors, process creation, browser launch, port binding, watcher startup, or stale state. Fix or report the environment issue first.
- **Product/test failure:** focused runner launches cleanly and reaches assertions, API/UI behavior, database state, or deterministic test setup. Then debug the code or test normally.
- **Mixed failure:** isolate the environment blocker first, then rerun the focused test before changing expectations or product code.

## Reporting

Be explicit about what was validated and what was not. If tests could not run because of local resource/process issues, say that plainly and include the observed evidence instead of claiming product validation.
