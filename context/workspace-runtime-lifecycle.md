# Workspace Runtime Lifecycle

Use this document for changes in workspace delete flows, runtime session
management, tmux persistence, and workspace terminal UI behavior.

## Remote execution context

- A controller supplies leased PR/issue launch context to a devbox; remote-only creation is
  repository plus branch. Expired context gates source-dependent launches, not shell launches,
  existing terminals, or Git operations (`internal/server/devboxes.go::registerDevboxProxy`).
- Source-dependent devbox reads renew context only on an explicit expiry response and retry once;
  fresh reads must not add broker calls.
- Worker refresh responses must retain push state so replacing the displayed workspace does not
  erase controller-verified commit attribution.

## Runtime Model

kenn-forge manages three different things; keep them distinct:

- The persisted workspace record and worktree.
- The base workspace `tmux` terminal, which is durable and reconnectable.
- Launched runtime sessions and the shell drawer: tmux-backed and reconnectable across
  restarts when tmux is available, ptyowner otherwise.

Processes on the execution host:

- The daemon owns runtime rows, recovery, idle stop, and deletion
  (`internal/server/workspaceapi`), and launches and attaches sessions (`internal/workspace/localruntime`).
- A tmux server or a detached `pty-owner` (Go, or the opt-in Rust pty-manager) hosts each terminal
  and outlives the daemon.
- `acp-owner` runs inside such a terminal, drives the ACP agent, and keeps its transcript, queue,
  and chat activity report.
- Coding-agent hooks run `kenn-forge agent-hook`, which relays to the daemon's activity store.

### ACP chats

- Use the Go ACP SDK for framing, dispatch, and wire types; Forge owns process lifecycle and
  chat state, not a second protocol decoder (`internal/workspace/localruntime/acp.go::startACPSession`).
- ACP agents run on the workspace's execution host, including devboxes and Fleet nodes;
  browser disconnects leave the process and accepted turn running.
- ACP option selections are remembered per configured client and execution host, across
  workspaces and restarts; restore the model before dependent options, skip choices no longer
  offered, and merge only agent-confirmed changes (`internal/workspace/localruntime/acp_settings.go`).
- Launch ACP options (quick actions) apply over remembered ones for that chat only and are never
  remembered; a reload keeps the chat's last confirmed values; unoffered choices become a notice.
- An ACP target with `disable_forge_tools` receives no Forge MCP endpoint or credentials,
  including in settings probes and when reattaching or replacing an owner.
- Codex ACP chats derive Full access (`dangerFullAccess`/`never`) or Read-only (`readOnly`/
  `on-request`) only in the adapter's default `agent` mode; saved, `INITIAL_AGENT_MODE`, and
  explicit modes win. Derived modes are never persisted (`internal/workspace/localruntime/acp_codex.go::deriveCodexMode`).
- Derivation probes only direct `codex-acp`, `codex-acp-{arch}-{os}`, `npx`, or `bunx` launches;
  other forms keep their mode silently. Probe failures, unmapped policies, and rejected modes keep
  the adapter default with a notice.
- Full access must hold without the adapter's project-trust override; untrusted project config
  can only lower access (`internal/codexhooks/access.go::readAccess`).
- ACP owners outlive the daemon under tmux/ptyowner and retain active turns, permissions, and
  chat state (`internal/workspace/localruntime/acp_owner.go::RunACPOwner`).
- Owner stderr vanishes with its terminal: every owner failure before it listens must reach the
  daemon as a start error with the agent's stderr tail, never as an attach timeout.
- On Windows the ACP agent shares its parent's console; only a parent with no console launches
  it with `CREATE_NO_WINDOW` (`internal/workspace/localruntime/process_windows.go::configureACPProcess`).
- Check an owner's protocol before attaching. Park and reload incompatible idle, resumable owners
  without losing transcript or native session; keep busy or non-resumable ones running with a
  recovery error. Bump the protocol for incompatible changes; never translate old frames.
- An owner parks its own idle, `loadSession` chat after 30 minutes unviewed (viewing rides the
  daemon's watch call), saving and disconnecting under `turnMu` so later input is refused unsent
  (`internal/workspace/localruntime/acp_idle.go::ACP.parkIfIdle`).
- A parked chat is no exit: the runtime lists it as parked as it drops it, so the prune keeps its row
  and activity still reads its report; the exit hook marks it recovery-pending and the next workspace
  read reloads it (`internal/workspace/localruntime/manager.go::Manager.removeExitedSession`).
- An owner whose state directory or socket is gone parks at once; owners outlive test daemons in
  the shared tmux server (`internal/workspace/localruntime/acp_idle.go::acpOwnerUnreachable`).
- Chats of an agent in `acpSharedAgents` share one host process per agent command; add an agent only
  after testing concurrent sessions, cwd, cancel and `session/close` against the real adapter. Others
  keep one process per chat, remembered per fingerprint (`internal/workspace/localruntime/acp_host.go`).
- Binding a chat checks the host by metadata only (command resolution, `/proc` file stamps, mise's
  current installs), never by starting the agent; a stale host keeps its chats and exits after the last
  (`internal/workspace/localruntime/acp_host_fingerprint.go::acpHostStale`).
- Stopping a chat closes its host connection, and the host cancels and closes only that session; a
  lost host parks its chats for a reload (`internal/workspace/localruntime/acp.go::ACP.wait`).
- A failed ACP attachment is not an agent exit: keep the pane and show the recovery error
  (`frontend/src/lib/components/acp/chat-session.ts::makeChatSession`).
- Reattach ACP only when its workspace opens, never at daemon startup; load the saved session
  only after its owner exits (`internal/server/workspaceapi/acp.go::Handler.restoreWorkspaceACP`).
- A session this daemon saw exit on its own is neither restored nor listed; only an owner that
  died while no daemon was attached is reloaded. A running owner is attached, never duplicated.
- Supervised chats are the exception: after their agent exits the record is kept as recovery
  pending, reported as exited until restored or stopped, and omitted from fleet inventory. The
  mark is in memory; after a restart a kept chat reloads like any orphaned chat.
- A taken-over chat is a person's chat: it is forgotten like any unsupervised chat and restore
  refuses it (`internal/server/workspaceapi/lifecycle.go::Handler.keepsExitedChat`).

#### Transcript and delivery

- The ACP owner keeps the whole transcript. Prompts and steering insert their user message where
  sending began, before output received during the send.
- Forge entry IDs, not optional or reused agent IDs, own transcript identity; ordering and
  versions must keep stale history from replacing newer updates
  (`frontend/src/lib/components/acp/chat-session.ts::makeChatSession`).
- Connect with a recent window, then send changed entries in byte-bounded batches. History loads
  only at the reader's request; updates never invalidate loaded history.
- Encode elicitation property maps deterministically; the form uses wire property order, so
  unchanged metadata must not reshuffle fields.
- Batches bound bytes and count but retain every message; one oversized message goes intact so
  paging always advances (`internal/workspace/localruntime/acp_delivery.go::acpMessageBatchSize`).
- Entry updates and history paging preserve expanded details and the reader's anchor.
- Natural agent exit leaves the attachment readable until viewers detach and pending RPC replies
  finish; explicit stop still cleans up.
- A degraded but usable start (no Forge tools, no session reload) is a notice, shown only as a
  header warning icon with a tooltip, never an error alert (maintainer decision).
- Pasted images survive queueing, steering, retries, and saved history; image-only prompts are
  valid; composer attachments clear only on acceptance.
- Publish streamed text only in finished markdown blocks, paced (400 ms) and flushed before tool
  activity, on quiet, and at turn end; never per token.
- Keep every ACP content type: a changed `messageId` starts a new message, and non-text blocks
  and thoughts are their own entries.
- Sub-agents come from legacy tool-call markers; the Go SDK cannot decode draft native subagent
  updates (`internal/workspace/localruntime/acp_client.go::toolCallLineage`).
- Command output arrives as Zed terminal tool-call metadata keyed by terminal ID.
- Do not suppress external URLs in ACP Markdown images; use normal browser loading (maintainer decision).
- Resolve media references on the agent host without workspace, symlink, or size limits, but read
  only regular files via Kit `fslink.ReadFile`; ACP has no video block, so video is an embedded
  blob resource (maintainer decision; `internal/server/workspaceapi/acp_media.go::acpContentMediaPreview`).
- Never cap or truncate ACP data in either direction; page older history instead. Stop and answers
  must never wait behind queued prompts, and every accepted prompt, including a deduplicated retry,
  is acknowledged; clients never infer acceptance from the transcript window.
- Owners, not the daemon, write chat activity reports so sidebar state survives daemon downtime;
  a reloaded session starts idle, never newly done.
- The saved transcript is the conversation of record: drop `session/load` replay and continue in
  a new session only when the agent never supported `loadSession`.
- A chat that previously supported native resume must fail rather than silently lose context; a
  coordinator-held chat never falls back to a new native session (`ErrACPCannotReload`), while a
  taken-over chat falls back like any person's chat.
- Project-worktree runtime APIs expose terminal targets only; ACP targets need the workspace chat
  transport (`internal/server/workspaceapi/projects_handlers.go`).

#### Queue, senders, and supervision

- A running turn never rejects input: sends queue, and people steer only when the agent
  advertises it. The queue drains one prompt per `end_turn` and pauses on any other stop, error,
  exit, or reload (`internal/workspace/localruntime/acp_queue.go::ACP.drain`).
- The head being sent is `sending`: it cannot be removed or moved and nothing moves ahead of it.
  A remove or move that arrives first always wins.
- Every prompt records its sender (`user`, or `coordinator` with its name) and names it to the
  agent under `_meta.forge`, since ACP has no notion of several senders.
- Input carrying a supervision generation, and MCP agent messages and spawns, are a coordinator's;
  everything else is a person's. Clients can never claim to be an agent (`ACPCommand.FromAgent`).
- The daemon log records each prompt's sender. The chat never shows a coordinator's prompt as the
  person's, and a prompt without a sender reads as the person's; a narrow pane labels a queued
  coordinator prompt only "Coordinator" (`frontend/src/lib/components/acp/chat-types.ts::senderLabel`).
- Each owner start gets a new runtime generation carried by permission and elicitation IDs, so an
  answer for an earlier process cannot reach a restarted agent. Answers are idempotent: the same
  answer succeeds, a different one fails. Published state never carries the answered ledger.
- A submission that was sending when its owner stopped, or was unqueued, never runs again; keep
  those ID lists uncapped for the session's life (`internal/workspace/localruntime/acp_submissions.go`).
- Supervision limits only the coordinator; people chat, steer, configure, answer, reorder, remove,
  and stop anything without a takeover (`internal/workspace/localruntime/acp_supervision.go`).
- A coordinator names the generation it holds; it may send or queue, remove only its own queued
  prompts, and stop or answer only in a turn its prompt started. Steering, reordering, and
  resuming a paused queue are person-only; an agent without the generation cannot add input.
- Claims may come any time; earlier input keeps its sender. A takeover only ends supervision and a
  person's always applies; a coordinator releasing must name its generation. Every claim or
  takeover advances the generation and stales all earlier ones.
- Queued prompts are not tied to supervision: an accepted coordinator prompt stays queued through
  any claim or takeover and runs in turn (maintainer decision).
- A stale-generation reply does not prove a prompt never ran; callers settle outcomes from the
  transcript and turn records.
- Only a supervisor's prompt may carry an active-time allowance, armed only if its sender still
  holds the chat when the prompt is written; a takeover disarms it (`internal/workspace/localruntime/acp_allowance.go`).
- Time blocked on a pending permission or elicitation is not active time. An exhausted allowance
  cancels a turn nobody has stopped once, and the turn ends normally; a person's stop never waits.
- A turn is blocked only while a request is pending and none of its tool calls runs; the tool call
  a pending permission asks to run, and subagent calls under it, count as waiting, not running.
- A steer never changes who owns the running turn, even when the agent answers with a new turn:
  the agent cannot tell senders apart (maintainer decision).
- A turn the agent starts after a steer keeps the chat busy from its first active status until
  idle, ends at once if the original prompt stopped or failed, and an agent that never reports
  status cannot leave the chat busy (`internal/workspace/localruntime/acp_queue.go::ACP.steerLocked`).
- Once the original prompt completed, any idle ends that turn: the SDK handles every status sent
  before a response before returning it.

### Terminal sessions

- The base workspace tmux session is durable backend state, but production terminal panes are
  pooled runtime sessions; the primary terminal endpoint is not a mounted SPA workflow
  (`frontend/src/lib/components/terminal/PooledSessionTerminal.svelte`).
- Launched agent and shell sessions are not durable after natural exit.
- The shell drawer is a singleton per workspace; a tmux-backed shell survives server restarts
  until it exits or the workspace is deleted.

### Agent activity reports

- Coding-agent session IDs are hook-authoritative and live-only: expose only fresh, supported
  reports matched to a live `agent` runtime.
- Hook reports own workspace activity where they exist: such a workspace is never probed by tmux
  pane capture until the session ends, its last activity is the report's timestamp, and reports
  have no time-based expiry.
- Reports belong to an agent, coding session, and runtime key; terminals sharing a coding session
  do not share activity or recovery records. SessionEnd and Remove clear only the caller's report.
- When Windows refuses to delete a report (sharing violation or access denied only), empty it
  instead; empty reports count as no activity and deletion is retried.
- SessionEnd fails loudly (HTTP 500; the hook CLI reports file and cause) when a report cannot be
  read, emptied, or deleted; a corrupt legacy report is left in place.
- Agents started before an upgrade can keep writing the old report filename; keep read and
  cleanup support until those sessions end (`internal/agentactivity/store.go::Store.legacyReportPath`).
- `done` over `done` keeps the first timestamp, so an acknowledged Done badge does not reappear
  when Claude Code's `idle_prompt` follows Stop.
- Claude Code's `idle_prompt` fires after a minute of waiting for anything. It maps to `done`
  only after a recorded `working` or `done` turn, so it cannot make an unprompted agent look
  resumable; only `elicitation_dialog` and user-input tools mean `input`.
- Only Stop refreshes pending background work and scheduled wakeups, and a concurrent hook must
  never overwrite Stop's result with a stale merge (`internal/agentactivity/store.go::Store.HandleEvent`).
- A report whose runtime row was pruned must not outlive it
  (`internal/server/workspaceapi/lifecycle.go::Handler.reconcileAgentActivityReports`).

### Worktree recovery

- Retry resumes setup in place and keeps branch, worktree, and runtime records; restarting a
  terminal must never erase local commits or dirty files (`internal/workspace/manager.go::Manager.RequestRetry`).
- A saved branch may have advanced past its original provider head; reattach it without
  resetting commits, and preserve it if later setup fails.
- A branch name alone (PR source branch or ad-hoc) never proves prior setup; recovery needs the
  existing checkout or its registration. Missing checkouts keep their Git registration and index
  through failed recovery, preserving detached commits (`internal/workspace/manager.go::Manager.addWorktree`).
- An empty managed branch means an adopted branch or detached HEAD; recover it from its registered
  HEAD without taking ownership. Roll back fresh failed checkouts but keep adopted source branches.

## Provider-Backed Lifecycle Facts

Launch specifications and source-visibility leases are owned by
[`fleet-architecture.md`](./fleet-architecture.md) (Provider Facts For Local Execution).

- Same-repository PRs fetch the hub-issued synthetic pull ref from the base clone. Fork PRs fetch
  the exact persisted fork clone URL and head branch; a spoke never derives a fork URL from
  provider cache state. Ad-hoc and Kata workspaces keep their local resolution paths.
- Generated agent context uses the last validated launch specification and may carry stale
  provider labels while the hub is unavailable.

## Terminal Transport

- Every terminal WebSocket leg, including Fleet relay upstream legs, negotiates RFC 7692
  `permessage-deflate` with context takeover; use standard negotiation, not an application
  compression envelope, so unsupported peers stay uncompressed (`internal/terminalwebsocket/`).
- The browser pane holding resize authority owns character and pixel geometry; hidden or
  non-owning panes cannot overwrite it. Retained PTYs use 8x16 only until geometry is claimed.
- PTY resize must hold the file descriptor across its ioctls and keep nonblocking I/O; terminal
  closure can overlap a resize (`internal/ptysize/resize_unix.go::Resize`).

## Natural Exit Rules

Natural process exit should collapse stale runtime state quickly.

- When a launched session exits naturally, remove it from backend runtime state and the UI; if it
  was active, select a remaining session, else show launch choices.
- Forget a tmux-backed session's persisted row once its backing tmux session is gone.
- When the shell drawer process exits, close the drawer, forget its row once tmux is gone, and
  require a fresh launch on reopen.
- PTY-owner exit may precede final output, and output EOF may precede process wait; keep a bounded
  drain and a bounded wait for the real exit code so the last repaint and status survive
  (`internal/ptyowner/owner.go::exitCodeAfterOutputClose`).
- The opt-in Rust PTY manager must keep the Go owner's exit-code, drain, linger, and stop
  semantics; change both owners together (`rust/pty-manager/src/main.rs::owner_should_exit`).
- Never close an owner connection without a response: clients read EOF as a dead owner and delete
  its state; at capacity, leave connections queued.

The base workspace `tmux` tab is the exception: keep reconnecting, and never auto-close it because
the websocket detached or the view remounted.

## Delete Ordering Rules

Workspace deletion is intentionally conservative, so a rejected delete never kills live sessions.

- Interactive DELETE checks dirtiness before durable admission; queued cleanup admits first and
  records a dirty refusal as `deletion_failed`.
- Persist deletion intent before destructive work; failures stay visible and retryable. Setup
  generations are process-local, so startup turns `creating` into `error` and `deleting` into
  `deletion_failed` (`internal/server/workspaceapi/lifecycle.go::Handler.Start`).
- Lifecycle writers acting on a previously observed status must use a conditional transition;
  tmux pruning cannot overwrite an admitted `deleting` (`internal/db/queries.go::DB.MarkReadyWorkspaceError`).
- Deletion blocks new launches before preflight without stopping sessions; runtimes stop only after
  preflight passes or force skips it, and destructive worktree and DB teardown follows only a
  successful runtime shutdown.
- Deletion clears activity reports and stop marks for every stored runtime, including parked ones.
- A confirmed delete publishes workspace absence before presenter-specific navigation; releasing
  the initiating presenter must not suppress tombstones, session cleanup, or route invalidation.
- DELETE and merge-cleanup admission are idempotent after authoritative absence; retries succeed
  without publishing another deletion.
- A merge that enqueues keeps its workspace deletion durably: a merged PR deletes on the owning
  daemon and retries refusals; a closed PR, or one confirmed unqueued after the request, drops it
  (`internal/server/pullapi/workspace_cleanup.go::Handler.resolveMergeWorkspaceCleanups`).
- Accepted by the maintainer: a later enqueue without deletion does not cancel a kept request, a
  failed save is only logged, and spoke preparation neither waits for nor hands off kept requests.

## Server Shutdown Ordering

- Spare-worktree registration finishes within its own bounded context; canceling Git mid-write can
  corrupt metadata every worktree uses (`internal/workspace/hot_worktree.go::Manager.prepareHotWorktree`).
- Fleet starts after Workspace and stops its workers before Workspace stops. Pull admission closes
  and its workers cancel before HTTP drain; Pull finishes before Fleet after drain
  (`internal/server/server.go::Server.Shutdown`).
- A timed-out stage must not let shutdown advance; a later call resumes at the blocked stage
  (`internal/server/streamapi/workspace_dependency_shutdown.go::WorkspaceDependencyShutdown`).

## Tmux Persistence Rules

- Startup attempts agent recovery before pruning missing runtime rows.
- The runtime row, not the activity report, records which conversation to resume; only hooks from
  the row's own worktree may set it (`internal/server/workspaceapi/agent_hook.go::Handler.recordAgentConversation`).
- Within one worktree another conversation replaces the saved one only after it ended or, after a
  restart, through a SessionStart; one that ended with reason `other` stays saved. Agents without
  SessionEnd hooks (Droid) save their newest conversation.
- A row created after its hooks reported adopts that conversation only when exactly one reported.
- Hooks do not identify their agent, so a nested agent in the same worktree can still become the
  saved conversation after a restart (accepted limit; process lineage is the robust signal).
- Recovery imports a reported conversation into rows that have none, for agents running before
  migration 000066; remove that import once no running agent can predate it.
- Missing tmux agents and gone ptyowner agents resume the saved Codex, Claude, Pi, Copilot, Cursor,
  Droid, or Gemini conversation by exact ID, keeping flags and runtime identity and never replaying
  the initial prompt; with nothing to resume they stay unavailable until stopped.
- Recovery enforces the same Git identity and credential-helper checks as a fresh launch, and keeps
  the conversation when validation fails so a repair can retry.
- Kit owns generated resume arguments; configured arguments pass through unchanged.
- Recovery never resets worktrees or resends the initial message; failed recovery keeps the row.
- Restore tmux base terminals before agents, including retained creating/error retries, within a
  shared startup budget that preserves unfinished attempts. Recovery never starts pty-owner base
  terminals, and activity enrichment reads an absent owner as idle without starting it.
- A tmux client exits 0 whenever its session ends, so the pane wrapper's exit record, not the exit
  code, decides: keep an agent that was killed, forget one that exited normally, except after a
  SessionEnd with reason `other` (agents can catch signals and exit 0) (`internal/workspace/localruntime/agent_exit.go`).
- A detached client also exits 0 while the agent runs on; it must not spend the exit record.
  Sessions launched without a record keep the old exit-code meaning.
- A client that exits non-zero lost its client or server, unless tmux reports its session gone
  while its server still runs (a failed launch).
- Runtimes without tmux have no record: their exit handler deletes the row at once, and an agent
  that finishes while Forge is down is still resumed, since its gone owner looks killed.
- Identify tmux servers by pid and start time, read in the same call that finds the session, so a
  replacement server never counts as the session ending; keep an agent whose server was never
  identified pending for the next start.
- Kept agents list as unavailable until Stop forgets them or the next start resumes them.
- Pruning deletes nothing while the tmux server has no sessions, judges each missing row only from a
  fresh listing, and errors no workspace while base terminals are outstanding
  (`internal/server/workspaceapi/workspace_enrichment.go::Handler.retainMissingRuntime`).
- After a tmux server loss, recovery restores every base terminal first, under setup admission so
  a deletion is never undone; terminals get only a share of each pass so a hung one cannot starve
  agents; unfinished workspaces stay outstanding, and the missing-tmux prune waits until none remain (`internal/server/workspaceapi/lifecycle.go::Handler.keepAgentAfterTmuxServerLoss`).
- Recovery changes must pass every shutdown order of `make test-reboot-sim`.
- Pending recovery retries during periodic pruning under setup admission, rotating sessions so a
  slow attempt cannot starve the rest (`internal/server/workspaceapi/lifecycle.go::Handler.restoreRuntimeSessions`).
- Explicit session stop is serialized with recovery and forgets the saved row.

### Idle runtime stop

- Idle stop has one owner (`internal/server/workspaceapi/idle_runtime.go`): every stop, resume, and
  stop mark goes through it, except mark removal on runtime/workspace deletion or tab close.
- A pty-owner workspace's runtimes stop only after `workspaces.idle_runtime_stop_hours` with no
  page view and no typing, counted from the latest of last use, workspace creation, and runtime
  creation. With the setting off, nothing is recorded, saved, or read.
- Turning the setting on starts every clock then. Turning it off marks stopped agents and ACP chats
  recovery-pending and a stopped shell `error`, and removes stop marks and the saved clock.
- Last use is saved periodically and at shutdown so a restart keeps idle time.
- A page view is the showing page's `viewing=true` runtime read. Typing is terminal input beyond
  automatic replies, a submitted agent message, or an ACP prompt or reply; socket opens,
  reconnects, focus, heartbeats, and other reads are neither.
- Command and tmux sessions never stop. Plain shells may; hook agents only with no working report,
  no pending background work or wakeups, a runnable target, and a resume path (`done`, `input`,
  `approval`, or `idle` after a `resume`/`fork` SessionStart).
- ACP chats stop only with `loadSession`; older-build owners that cannot park keep running (no-compat rule).
- Stopping keeps the saved conversation and row; an owner that survives a failed stop is unmarked
  and reattached.
- The pty-owner workspace terminal stops while idle only with no claimed socket; it stays ready and
  starts a fresh shell on attach (`internal/terminal/handler.go::Handler.StopUnattachedTerminal`).
- Only a page view resumes a stopped row: agents resume their conversation, shells start fresh,
  ACP chats reload. Resume runs in the background so the viewing read returns at once.
- A failed resume clears the mark and marks the row recovery-pending; agents and ACP chats retry,
  and a shell reads as `error`.
- Sockets, reads, and MCP list a stopped row as `parked`; startup recovery skips it.

### Launch compensation

- Removal of a created backend is attempted when launch or persistence fails; it is best-effort and
  not retried, and a survivor is unrecorded until startup reaping.
- Reaping must protect stored workspace, host, and project-worktree sessions while recognizing every
  owned scope-derived tmux name (`internal/workspace/manager.go::ReapOrphanTmuxSessions`).
- Command and per-worktree shell ensure hold one transaction through persistence and compensation
  so rollback cannot race adoption (`internal/workspace/localruntime/command_session.go::EnsureCommandSessionAndPersist`).
- Persistence failures stay client-visible; compensation failures are logged. A stale creator
  rollback must never kill an adopted backend (`internal/workspace/localruntime/manager.go::RollbackLaunch`).
- A runtime can exit while its metadata write is in flight, so every launch path reconciles after
  persistence: live means both key and `CreatedAt` match, and a replacement must not keep the exited
  generation's row.

### Shutdown and restart detach

- Normal server shutdown is not a natural user exit: it keeps terminal tabs available for
  reconnection, including an intentional stop then start (`internal/workspace/localruntime/manager.go::Manager.Shutdown`).
- `KENN_FORGE_DEV_RESTART` is an opt-out: unset, empty, or `1` enables restart-detach; other values
  disable it, except on execution workers (`cmd/kenn-forge/main.go::detachRuntimeSessionsForRestart`).
- Genuine exits and explicit session stops still close their tabs.
- Supervisors must leave durable tmux and ptyowner processes alive; Forge-managed systemd units use
  `KillMode=process` (`internal/fleetsetup/service.go::renderSystemdUserService`).

### Tmux server and environment

- New tmux sessions use the `forge-` prefix; persisted `middleman-` names stay valid and are never
  rewritten.
- Check a tmux session's existence with an exact `=name` target; tmux otherwise prefix-matches a
  runtime session's name for a missing base (`internal/workspace/manager.go::Manager.tmuxSessionExists`).
- Only `DefaultTmuxCommand` selects Forge's dedicated tmux server. Custom commands may address a
  shared user server, so Forge never changes their global options or graphics settings.
- Dedicated servers enable application OSC 52 forwarding; `external` drops agent copies before the
  browser can handle them (`internal/workspace/manager.go::ApplyTmuxClipboard`).
- The tmux server keeps its spawn environment forever and every pane can read it, so Forge's tmux
  clients run with a deny-by-default non-secret allowlist, never a strip list; panes (including the
  base terminal's credential-sanitized full environment) get theirs only through the env-file handoff (`internal/workspace/localruntime/manager.go::TmuxClientEnvironment`).
- The allowlist names exact variables, never prefixes, minus configured token names under admitted
  `LC_`/`XDG_` prefixes. It must keep `TMUX_TMPDIR`, or tmux resolves `-L` sockets to a different
  server (`internal/config/config.go::IsTmuxNonSecretEnvVar`).
- Attach commands returned to external callers wrap in `env`, unsetting `TMUX` and matching the
  daemon's `TMUX_TMPDIR`.
- Config and Kata catalog validation reject `token_env` names in the allowlist, because a retained
  spawn environment cannot be scrubbed after a name turns secret; catalog names also feed every
  credential strip set at boot (`internal/config/config.go::Config.validateTokenEnvNamesNotTerminalVars`).
- Keep terminal handoff files in Forge state storage; shared temp exhaustion must not block them.
- Every attach passes `-E` (otherwise the attach client's variables leak into the session) and
  forces UTF-8 (service launchers may omit locale) (`internal/workspace/localruntime/tmux_launcher.go::tmuxAttachSessionCommand`).
- On macOS, shells with no `LANG`, `LC_ALL`, or `LC_CTYPE` default `LC_CTYPE` to UTF-8 without
  overriding an explicit locale; launchd otherwise breaks zsh prompt widths.
- Resolve the base terminal's login shell from the user database on every launch (macOS: `id -P`)
  and skip a non-executable one; the daemon's `$SHELL` is fixed at start, and a removed shell makes
  every new pane exit at once (`internal/workspace/manager.go::userLoginShell`).
- The dedicated server owns global passthrough, SIXEL, and mouse mode, and live changes clear pane
  overrides; custom servers get
  passthrough only on Forge-owned panes while graphics are on (`internal/workspace/manager.go::Manager.ApplyTmuxGraphics`).
- Startup applies saved graphics and mouse settings before restoring retained clients; live changes
  are best-effort and setup reapplies pane state.
- Enabling graphics installs each replacement retained client before detaching the old one, so a
  reconnect always finds an attachment.
- Terminal graphics are optional and default on: SIXEL and iTerm images; Kitty stays disabled while
  the xterm add-on labels it alpha.
- Retained tmux attach PTYs must start with nonzero pixel geometry; tmux asks for pixels before a
  browser subscribes and never retries the query native SIXEL needs.

### Terminal input and replay

- Reconnects restore browser-generated cursor-key, mouse, focus, and paste DEC modes from
  session-wide PTY state, not bounded screen replay (`internal/workspace/localruntime/manager.go::session.subscribe`).
- Pass initial prompts to directly configured Claude commands as CLI arguments; its trust dialog
  uses raw input, so a synthetic Enter can select exit.
- Reject oversized Windows launch commands before starting anything, counting quoted UTF-16
  arguments including helper JSON (`internal/ptyowner/client.go::Client.Ensure`).
- Terminal prompt delivery requires observed bracketed-paste mode, writes the whole paste in one
  write, then Enter as a separate write after a settle delay; agent TUIs treat same-chunk bytes as
  part of the paste (`internal/workspace/localruntime/manager.go::session.submitInitialMessage`).
- The paste-and-Enter sequence holds the session input lock and runs to completion even past the
  caller's deadline. Do not gate Enter on echo: Claude renders pasted input only after Enter.
- Tmux advertises bracketed paste before its agent starts; also require the pane to disable
  canonical input and echo, or startup can discard the prompt.
- Mode transitions precede one session-wide UTF-8-aware VT tail even in the alternate screen;
  retain split-rune introducers and decoded C1 controls/ST.
- Mode replay must mirror xterm.js 6: DEC private save/restore and ignored 1005/1015 encodings
  cannot re-enable modes or displace 1006/1016.
- Recognize C1 CSI only as decoded U+009B; invalid scalars, BOM, and stray continuation bytes are
  discarded before the VT parser (`internal/workspace/localruntime/terminal_input_modes.go::terminalInputModeState.observe`).
- In xterm.js 6, DECSTR resets cursor keys, focus reporting, and bracketed paste but not the mouse
  protocol and encoding.
- Send reconnect cancellation to xterm as bytes, not text; only binary input clears its UTF-8 decoder.
- Direct local-runtime sessions withhold resize/refresh until xterm parses a subscriber-only replay
  boundary, queued only once the VT/UTF-8 tail reaches ground; legacy and Fleet attachments still
  get dimensions on every connection.

## UI Contract Rules

The workspace UI should reflect runtime truth without leaving users stranded in stale tabs.

- `/workspaces/{id}/runtime` is the authoritative view of live launched sessions. The frontend may
  react to exit events immediately but then reconciles with a runtime refresh.
- Queued launch admission reads runtime after workspace readiness; presentation caches never
  supply its reconciliation baseline (`frontend/src/lib/components/terminal/workspace-runtime-workflow.ts::executeMutation`).
- Once a ready event advances setup, older `creating` details must not hide the terminal or stall
  queued launch.
- A ready event never overrides fresh setup reads or authoritative error and deletion responses.
- Manual stop bounds every awaited stage and publishes confirmed absence before best-effort refresh;
  nothing may leave the control pending forever.
- Publish confirmed sessions before reloads but keep them provisional: only a successful runtime
  read may tombstone absent peers; reject only definite failures.
- Workspace terminals use xterm.js exclusively; there is no renderer setting.
- Clients replace silent half-open sockets without a reload; every terminal backend answers the
  shared heartbeat and Fleet bridges pass it through (`internal/terminalwebsocket/terminalwebsocket.go::WriteHeartbeat`).
- Own every non-empty text paste at the terminal container, sanitize, and send it once; never
  delegate single-line paste to xterm. It must work on insecure HTTP without the async Clipboard API.
- Browser text wins over image data; image-only paste uploads to the terminal's host and pastes the
  returned paths.
- Treat terminal processes as native-terminal-equivalent, but accept bounded, write-only OSC 52
  writes only after one recent trusted DOM gesture; terminal data callbacks are not input
  provenance. Browser denial falls back through CSRF-protected loopback.
- Keep OSC 52 validation synchronous and ahead of gesture consumption; `@xterm/addon-clipboard`
  cannot preserve current rejection and read-denial guarantees.
- Clipboard authority is revoked on external pointerdown or focus transfer, pane inactivity or
  parking, disablement, or window focus loss, including in-flight loopback fallbacks.
- Pointerdown only prepares clipboard access; selection intent needs a four-CSS-pixel dead zone and
  about one cell of movement, so focus-click jitter never reaches clipboard fallbacks
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::hasPointerSelectionIntent`).
- Captured drags keep authority only through internal or destinationless focus movement while the
  pane stays active; their watchdog also releases browser capture.
- Host clipboard writes require a local browser; a trusted loopback proxy must report exactly one
  client IP assigned to the host (`internal/server/authapi/terminal_clipboard_access.go::IsLocalTerminalClipboardRequest`).
- During tmux SGR drags outside xterm bounds, add only clamped edge reports, forward everything
  else unchanged, and never keep unsent drag state across a WebSocket boundary.
- Agent TUIs in the normal buffer with no scrollback receive vertical wheel as cursor input;
  xterm/tmux keep it with scrollback, alternate buffer, mouse tracking, or Ctrl-wheel zoom.
- xterm's post-lift `-xterm-gesturechange` events omit coordinates; pin them to the last finger
  position so fling momentum never reaches apps as `NaN` wheel reports.
- macOS loopback clipboard runs `pbcopy` with `LC_ALL=en_US.UTF-8`; Windows sends UTF-16LE to
  `clip.exe` (`internal/systemclipboard/systemclipboard.go`).
- Only the active pane publishes cell geometry; font-metric changes must refit explicitly because
  unchanged container pixels never trigger resize observation.
- Keyboard and pointer interactions on nested controls inside workspace rows must not navigate.
- Selected tabs persist on the execution host and restore across browsers; browser storage is only a
  cache. Restoration and automatic focus never overwrite a saved choice; promoted agent focus counts
  as selection.
- Promoted terminal-region sessions remember `terminal` without changing detail-pane placement and
  open its top dock on entering Workspaces; resolve saved keys against the browser's current region.
- Order selection requests per workspace and host; a delayed peer must not block another workspace.

## Released Terminal Retention

- Workspace presentation and terminal retention have separate limits; a retained socket alone
  cannot restore an evicted workspace view.
- Repeat visits restore host-scoped presentation before revalidation; cached runtime may reclaim
  retained sessions but never decides queued launches, authorizes attachments, or discards peers.
- Event-stream `Open` after initial loading is not runtime invalidation; `ReconnectStale` still
  reloads workspace and runtime.
- Parked workspace hosts stop periodic runtime reads unless a promoted pane stays visible, without
  stopping launch reconciliation.
- A live view claims its sessions even in hidden tabs; only unclaimed sessions enter the bounded
  LRU, and a zero limit disables retention (`frontend/src/lib/stores/session-host.svelte.ts::noteSessionReleased`).
- While a switch awaits destination reconciliation, cache trimming must protect the destination.
- Retention keeps the parsed xterm subtree and socket but gives up interaction, resize, and WebGL;
  reclaim reparents the same subtree without reconnect or replay.
- Released sessions are presentation cache, never runtime authority: disconnect, exit, deletion,
  or stale generation discards them rather than reconnecting in the background.

## Shell Command Override

- Without tmux, the plain shell launches through ptyowner, so its lifetime is decoupled from the
  server.
- Hardened systemd units (`SystemCallFilter=~@privileged` and similar) break zsh and bash, which
  call `setresuid` at startup; wrap `[shell] command` (and independently `[tmux] command`) in, for
  example, `systemd-run --user --property=KillMode=process` to escape the unit's filter.
- The wrapper must forward the working directory; a `systemd-run` transient unit does not inherit it.
- The argv runs verbatim; the first element must be absolute or `PATH`-resolvable, never relative,
  so a malicious worktree cannot plant a binary. Unset falls back to `$SHELL`, then `/bin/sh`.

## Switch-Timing Instrumentation

- `workspace-switch:<phase>` User Timing names are stable API for performance comparisons: never
  rename them, and record new phases through `frontend/src/lib/instrumentation/workspaceSwitchTiming.ts`.
- `make profile-workspace-switch` captures a reproducible profile; each measure's `detail.traceId`
  joins the server OTel trace, whose export is opt-in via `OTEL_TRACES_EXPORTER`.
- Every frontend HTTP path, including hand-written runtime requests, uses the shared traced fetch
  boundary (`frontend/src/lib/api/runtime.ts::tracedFetch`).
- Base-path routing must preserve the inner Huma route pattern for OTel, or prefixed spans collapse
  (`internal/server/otelmiddleware/otel_middleware.go::StripPrefixPreservingPattern`).
- Automatic HTTP tracing excludes only exact long-lived stream routes; short endpoints stay traced.

## Testing Expectations

Prefer full-stack coverage when a bug crosses backend lifecycle and frontend behavior.

- Use real SQLite-backed server tests for delete ordering, tmux cleanup, and runtime-session API
  behavior; use tmux wrappers or fakes for missing-session and dead-server cases.
- Root-retained Git tests must cross a root composition boundary and take the root Git semaphore;
  `t.Parallel` alone never bounds Git work (`internal/server/api_test.go::acquireRootWorkspaceGitSlot`).
- A server test that creates a workspace must wait for setup to finish before returning; the
  background setup otherwise races `t.TempDir` cleanup ("directory not empty").
- Workspace `ready` can precede the pushed-head observer pass; tests that mutate upstream after
  readiness must use a state the observer cannot repair.
- Spoke lifecycle tests seed a launch specification and leave provider-item tables empty;
  lease-expiry tests use the handler clock so every layer sees the same instant.
- Kata API fixtures use their package-private tmux server and force-delete workspaces; shutdown
  preserves base sessions, so temp-dir cleanup alone leaks.
- Tests of the unconfigured tmux default sandbox `TMUX_TMPDIR` and probe the `kenn-forge` socket
  name literally, never derived from the default command.
- Fake tmux fixtures bake their control paths into the script, not the client environment.
- Federation terminal tests must cross an authenticated TLS boundary.

Related intent docs:

- [`context/workspace-apis.md`](./workspace-apis.md) for workspace API scope and
  non-goals.
- [`context/ui-interaction-contracts.md`](./ui-interaction-contracts.md) for
  row/button, tab, and keyboard interaction expectations in the UI.
