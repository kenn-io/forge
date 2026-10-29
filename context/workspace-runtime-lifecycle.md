# Workspace Runtime Lifecycle

Use this document for changes in workspace delete flows, runtime session
management, tmux persistence, and workspace terminal UI behavior.

## Remote execution context

- A controller supplies leased PR/issue launch context to a devbox; remote-only creation is
  repository plus branch. Expired context gates source-dependent launches, not shell launches,
  existing terminals or Git operations. (`internal/server/devboxes.go::registerDevboxProxy`)
- Source-dependent devbox reads renew context only on an explicit expiry response and retry once;
  fresh reads must not add broker calls. (`internal/server/devboxes.go::registerDevboxProxy`)
- Worker refresh responses must retain push state so replacing the displayed workspace does not
  erase controller-verified commit attribution. (`internal/server/workspaceapi/routes_handlers.go::Handler.refreshWorkspace`)

## Purpose

- Keep the lifecycle of kenn-forge-managed runtime state explicit.
- Preserve the distinction between the durable workspace, the base tmux
  terminal, and launched runtime sessions.
- Prevent review regressions around destructive ordering, stale tmux rows, and
  UI/runtime disagreement after exits.

## Runtime Model

kenn-forge manages three related but different things:

- The persisted workspace record and worktree.
- The base workspace `tmux` terminal, which is durable and reconnectable.
- Launched runtime sessions and the shell drawer. When tmux is available they
  are tmux-backed, recorded, and reconnectable across kenn-forge restarts; when
  tmux is unavailable they use ptyowner.

Rules:

- Use the Go ACP SDK for protocol framing, dispatch, and wire types; Forge owns
  process lifecycle and chat state, not a second protocol decoder
  (`internal/workspace/localruntime/acp.go::startACPSession`).

- ACP agents run on the workspace's selected execution host, including devboxes and
  Fleet nodes; browser disconnects leave the process and accepted turn running
  (`internal/workspace/localruntime/acp.go::ACP`).
- ACP selections belong to a configured client on its execution host, across workspaces
  and daemon restarts; restore the model before dependent options and skip choices no
  longer offered by the agent. Concurrent sessions merge only agent-confirmed changes
  into the latest saved preferences (`internal/workspace/localruntime/acp_settings.go`).
- Launch ACP options (quick actions) apply over remembered ones for that chat only and are
  never remembered; a reload keeps the chat's last confirmed values, and unoffered choices
  become a notice (`internal/workspace/localruntime/acp_settings.go::Manager.startACP`).
- An ACP target with `disable_forge_tools` receives no Forge MCP endpoint or credentials;
  preserve that choice in settings probes and when reattaching or replacing an owner
  (`internal/workspace/localruntime/acp_mcp_proxy.go::acpMCPProxy`, `internal/workspace/localruntime/acp.go::Manager.TestACP`).
- Codex ACP chats derive Full access for `dangerFullAccess`/`never` and Read-only for `readOnly`/`on-request` only while the adapter is in its default `agent` mode; saved offered modes, non-empty session `INITIAL_AGENT_MODE`, and explicit adapter modes win.
  Probe only direct `codex-acp`, `codex-acp-{arch}-{os}`, `npx`, or `bunx` launches, using the session's executable resolution; other launch forms keep their mode without a notice.
  Derived modes stay unpersisted; probe failures, unmapped non-workspace-write policies, and rejected derived modes keep the adapter default with a notice
  (`internal/workspace/localruntime/acp_codex.go::deriveCodexMode`).
  Full access needs the same result without the adapter's project-trust override; untrusted project config can only lower access (`internal/codexhooks/access.go::readAccess`).
- ACP owners and their SDK connections survive daemon shutdown under tmux/ptyowner;
  retain active turns, permissions, and chat state (`internal/workspace/localruntime/acp_owner.go::RunACPOwner`).
- Owner and agent stderr vanish with the owner's terminal. Every owner failure before it listens
  must reach the attaching daemon through the start-error record, with the agent's error and stderr
  tail, never as the daemon's attach timeout (`internal/workspace/localruntime/acp_owner.go::recordACPStartError`).
- On Windows the ACP agent shares its parent's console: acp-owner's ConPTY for workspace
  agents, or the daemon's console for the settings test handshake (`Manager.TestACP`). Only a
  parent with no console launches it with `CREATE_NO_WINDOW` (`internal/workspace/localruntime/process_windows.go::configureACPProcess`).
- ACP owners outlive the daemon. Check their protocol before attachment; park and reload
  incompatible idle, resumable owners without deleting their transcript or native session.
  Active/pending work and owners without confirmed resume support stay running with an
  actionable recovery error. Increment the owner protocol for incompatible RPC/frame changes;
  do not translate old frames (`internal/workspace/localruntime/acp_upgrade.go::Manager.replaceACPOwner`).
- A failed ACP attachment is not an agent exit: retain the pane and show the recovery
  error even when the runtime list already marks the chat as errored
  (`frontend/src/lib/components/acp/chat-session.ts::makeChatSession`).
- Reattach ACP only when its workspace opens, never at daemon startup; load the saved
  ACP session only after its owner exits (`internal/server/workspaceapi/acp.go::Handler.restoreWorkspaceACP`).
- A session this daemon saw exit on its own is neither restored nor listed, even before its
  stored record is forgotten; only an owner that died while no daemon was attached is reloaded
  (`internal/workspace/localruntime/manager.go::Manager.Exited`). Supervised chats are the
  exception: their coordinator may restore one after its agent exits, from the daemon's stored
  record so the backend check targets the launched backend; a running owner is attached, never
  duplicated (`internal/workspace/localruntime/acp_owner.go::Manager.RestoreSupervisedACP`).
- When a supervised chat's agent exits, the daemon keeps its stored record and marks it
  recovery pending, so neither the exit hook nor the missing-backend prune forgets it; the chat
  route reports it as exited until restored or stopped, and the fleet inventory omits it. A
  taken-over chat keeps its supervision record but is a person's chat: it is forgotten like
  any unsupervised chat, and restore refuses it. The exited mark is in memory:
  after a daemon restart, opening the workspace reloads a kept chat like any chat whose owner
  died while no daemon was attached. Unsupervised chats are forgotten as before
  (`internal/server/workspaceapi/lifecycle.go::Handler.keepsExitedChat`).
- The ACP owner keeps the whole transcript. Prompts and steering insert their user message
  where sending began, before output received during the send
  (`internal/workspace/localruntime/acp_queue.go::ACP.steerLocked`).
- Forge entry IDs, not optional or reused agent IDs, own transcript identity. Predecessor
  IDs preserve ordering through insertion; versions prevent stale history replacing updates
  (`frontend/src/lib/components/acp/chat-session.ts::makeChatSession`).
- Connect with a recent window, then send changed entries in batches bounded by serialized bytes. History
  loads only at the reader's request; updates never invalidate and reload loaded history
  (`frontend/src/lib/components/acp/chat-session.ts::makeChatSession`).
- Encode elicitation property maps deterministically in snapshots and updates; the form
  uses wire property order, so unchanged metadata must not reshuffle fields
  (`internal/workspace/localruntime/acp_changes.go::encodeACPBatch`).
- Chat delivery batches target serialized bytes as well as message count; retain every
  message and allow one oversized message intact so paging always advances
  (`internal/workspace/localruntime/acp_delivery.go::acpMessageBatchSize`).
- Entry updates and history paging must preserve expanded details and the reader's anchor;
  results and controls keep advancing during continuous batches
  (`frontend/src/lib/components/acp/ACPWorkspace.svelte`).
- Natural agent exit must leave the attachment discoverable and readable until existing
  viewers detach and pending RPC replies finish; explicit stop must still clean up
  (`internal/workspace/localruntime/acp_owner_rpc.go::acpAttachment.watch`).
- A degraded but usable ACP start (no Forge tools, no session reload) is a `notices`
  entry, never `error`; the chat shows it only as a header warning icon with a tooltip,
  not a persistent alert (maintainer decision; `frontend/src/lib/components/acp/ACPWorkspace.svelte`).
- Pasted images stay attached through ACP queueing, steering, retries, and saved history;
  image-only prompts are valid, and composer attachments clear only on acceptance
  (`frontend/src/lib/components/acp/ACPWorkspace.svelte::settlePending`).
- A running ACP turn never rejects input: sends queue, and people steer only when initialize
  advertises it. The queue drains one prompt per `end_turn` and pauses on any other stop,
  error, exit, or reload. Drain sends only the current head of an unpaused queue, reserving it
  as sending under the lock that checks it, so a removed or moved head or a Stop leaves the
  rest of the queue as it is; it stays queued until its write returns, but cannot be removed or moved
  (`sending`), and nothing moves ahead of it. A remove or move before the reservation always
  wins (`internal/workspace/localruntime/acp_queue.go::ACP.drain`).
- Every prompt records its sender, `user` or `coordinator` with the coordinator's name when
  known, on its queue entry, transcript message, and turn record (entries saved before senders
  existed restore as a person's), and names it to the agent
  under `_meta.forge` on `session/prompt` and steering requests, since ACP has no notion of
  several senders. Input carrying a supervision generation, and MCP agent messages and spawns,
  are a coordinator's; everything else is a person's. `ACPCommand.FromAgent` is never decoded
  from JSON, so clients cannot claim to be an agent. The daemon log records each prompt's
  sender as it passes the owner attachment
  (`internal/workspace/localruntime/acp_owner_rpc.go::acpAttachment.Command`). The chat never
  shows a coordinator's prompt as the person's; a prompt without a sender predates senders
  and reads as the person's (`frontend/src/lib/components/acp/chat-types.ts::senderLabel`). A
  narrow pane labels a queued coordinator prompt only "Coordinator", without its name.
- Every ACP owner start gets a new runtime generation, and permission and elicitation IDs
  carry it, so an answer for an earlier process cannot reach a restarted agent. Answers are
  idempotent: the same answer to an answered request succeeds, a different one fails, and
  each turn keeps a short record of its submission ID and outcome. The answered ledger stays
  in the owner and its saved session; published state never carries it
  (`internal/workspace/localruntime/acp_requests.go`, `acp_submissions.go`).
- A submission ID that was sending when its owner stopped, or was unqueued, never runs
  again; keep both ID lists uncapped for the session's life, since evicting one would let a
  retry repeat or revive work (`internal/workspace/localruntime/acp_submissions.go`).
- Supervision limits only the coordinator; people chat, steer, configure, answer requests,
  reorder and remove anything queued, and stop any turn without a takeover. A coordinator's
  input names the generation it holds; it may send or queue, remove only its own queued
  prompts, and stop or answer requests only in a turn its own prompt started. Steering,
  reordering, and resuming a paused queue are people's (`person_only`), and an agent without
  the generation cannot add to a supervised chat. A claim may come at any time, since earlier
  input keeps its sender. A takeover only ends supervision; a person's always applies, and a
  coordinator releasing its claim must name the generation it holds. Every claim or takeover
  advances the generation and leaves every earlier generation stale, including the one a
  takeover produced. Queued prompts are not tied to supervision: a coordinator's prompt
  accepted into the queue stays queued through any claim or takeover and runs in its turn,
  bounded by its allowance only if its sender still holds the chat (maintainer decision). Each command checks the generation
  under the lock that guards its change; a prompt checks again when it starts. A
  stale-generation reply does not prove a prompt never ran: the gate precedes the retry check,
  so callers settle outcomes from the transcript and turn records
  (`internal/workspace/localruntime/acp_supervision.go`).
- Only a supervisor's prompt may carry an active-time allowance. It travels with the queued
  prompt, is armed only if its sender still holds the chat once the prompt is written, and a
  takeover disarms it for the running turn. A turn is blocked while a permission or elicitation is pending and
  none of its tool calls runs; a tool call a pending permission asks to run, and the subagent
  tool calls it runs under, are waiting, not running. Elicitations name no tool call. Blocked
  time is not active time. An exhausted allowance cancels a turn nobody has stopped once,
  and the turn ends normally. ACP cancels whatever turn runs, so the allowance's cancel, like
  a coordinator's, checks that its turn still runs and sends under `turnMu`, which every new
  turn starts under; a person's stop never waits. A turn a late agent status starts without
  `turnMu` can still meet such a cancel within microseconds; that visible, recoverable race is
  accepted (maintainer decision). A steer never changes who owns the running turn, even when
  the agent answers it with a new turn: the agent cannot tell senders apart (maintainer
  decision)
  (`internal/workspace/localruntime/acp_allowance.go`).
- A turn the agent starts after a steer keeps the chat busy from its first active thread
  status until the thread goes idle, even when that status arrives after the prompt completed.
  Once the original prompt has completed, any idle ends that turn, active or not: the SDK
  handles every status sent before a response before returning it. A stopped or failed
  original prompt ends the takeover turn at once.
  Until a status confirms it, the turn ends with the prompt, so an agent that never reports
  status cannot leave the chat busy (`internal/workspace/localruntime/acp_queue.go::ACP.steerLocked`).
- ACP owners, not the daemon, write chat activity reports so sidebar state survives
  daemon downtime; a reloaded session starts idle, never newly done
  (`internal/workspace/localruntime/acp_activity.go::reportACPActivity`).
- Sub-agents come from legacy tool-call markers; the Go SDK cannot decode draft native
  subagent updates (`internal/workspace/localruntime/acp_client.go::toolCallLineage`).
- Publish streamed ACP text only in finished markdown blocks, paced at 400 ms and released
  before tool activity, when the agent goes quiet, and at turn end; never per token
  (`internal/workspace/localruntime/acp_delivery.go::ACP.deliverTextLocked`).
- A saved chat that previously supported native resume must fail rather than silently lose
  agent context if the replacement no longer supports it
  (`internal/workspace/localruntime/acp.go::startACPSession`).
- The saved ACP transcript is the conversation of record: drop `session/load` replay, and
  continue in a new session only when the agent never supported `loadSession`. A chat a coordinator holds
  never falls back to a new native session: the start fails with `ErrACPCannotReload`, which
  the owner records for the waiting daemon, and the saved session stays. A taken-over chat
  falls back like any person's chat
  (`internal/workspace/localruntime/acp.go::startACPSession`).
- Keep every ACP content type: a changed `messageId` starts a new message, and non-text blocks
  and thoughts are their own entries (`internal/workspace/localruntime/acp_client.go::ACP.appendContentLocked`).
- Do not suppress external URLs in ACP Markdown images; use normal browser image loading
  (maintainer decision; `frontend/src/lib/components/acp/ChatMessageView.svelte::decorate`).
- Resolve image, video, and audio references on the agent host for live and saved chat, without
  workspace, symlink, or size restrictions, but read only regular files (symlinks resolved first)
  through Kit's `fslink.ReadFile`; pipes, sockets, and devices can block or never end. ACP has no
  video block, so video becomes an embedded blob resource (maintainer decision;
  `internal/server/workspaceapi/acp_media.go::acpContentMediaPreview`).
- Never cap or truncate ACP data in either direction (prompts, commands, plans, media, tool
  output, errors, websocket frames, command backlogs); a session too large for the UI pages older
  history in. The chat socket's reader never waits on queued prompts, so a stop or answer
  always gets through, and every accepted prompt, including a deduplicated retry, is
  acknowledged to its sender; clients never infer acceptance only from the transcript window
  (`internal/server/workspaceapi/acp.go::serveACP`).
- Command output arrives as Zed terminal tool-call metadata (`terminal_output_delta`,
  `terminal_exit`), keyed by terminal ID (`internal/workspace/localruntime/acp_content.go::applyTerminalMeta`).
- Project-worktree runtime APIs expose terminal targets only. ACP targets require
  the workspace chat transport and are neither listed nor launchable
  through project-worktree runtime routes (`internal/server/workspaceapi/projects_handlers.go`).

- The base workspace tmux session remains durable backend state, but production
  workspace terminal panes are pooled runtime sessions; do not treat the primary
  terminal endpoint as a mounted SPA workflow (`frontend/src/lib/components/terminal/PooledSessionTerminal.svelte`).
- Launched agent sessions and shell sessions are not durable after natural exit.
- The shell drawer is a singleton per workspace, but a tmux-backed shell should
  survive kenn-forge server restarts until the shell exits or the workspace is
  deleted.
- Coding-agent session IDs are hook-authoritative and live-only: expose only fresh,
  supported reports joined by canonical worktree and runtime key to a live `agent`
  runtime (`internal/server/workspaceapi/agent_sessions.go::Handler.listWorkspaceAgentSessions`).
- Hook reports own workspace activity where they exist: a workspace with a
  hook report for a live agent runtime is never probed through tmux pane
  capture, its last-activity time is the report's timestamp, and the tmux
  probe returns only when the session ends or its runtime is removed. Reports
  have no time-based expiry; a launched agent keeps reporting until teardown
  (`internal/server/workspaceapi/routes_handlers.go::Handler.applyWorkspaceTmuxEnrichment`,
  `internal/agentactivity/store.go::Store.LiveReportsForWorkspace`).
- Reports belong to an agent, coding session, and runtime key; two terminals
  can share a coding session without sharing activity or recovery records
  (`internal/agentactivity/store.go::Store.reportPath`).
- SessionEnd and Remove clear only the calling terminal's report
  (`internal/agentactivity/store.go::Store.Remove`).
- When Windows refuses to delete a report, cleanup empties it instead
  (`internal/agentactivity/store.go::Store.Remove`):
  - Only sharing-violation and access-denied delete errors permit emptying
    (`internal/agentactivity/report_lock_windows.go::isReportLockError`).
  - Empty reports count as no activity. Every scan skips them and retries
    their deletion, including after a daemon restart. They stay out of the
    cache comparison, so they do not invalidate the cached remaining reports
    (`internal/agentactivity/store.go::Store.reports`).
  - SessionEnd deletes an empty legacy report whichever runtime ends, and
    leaves a corrupt or invalid legacy report in place.
  - SessionEnd fails with HTTP 500, and the hook CLI exits with the file and
    cause, when a legacy report cannot be opened or read for a reason other
    than absence, a nonempty report cannot be emptied, or deletion fails for
    any other reason
    (`internal/server/workspaceapi/agent_hook.go::Handler.receiveAgentHook`,
    `cmd/kenn-forge/agent_hook.go::agentHookRelay.relay`).
- Agents started before an upgrade can keep writing the old report filename;
  retain read and cleanup support until those sessions end
  (`internal/agentactivity/store.go::Store.legacyReportPath`).
- A completion keeps its first timestamp: `done` written over `done` for the
  same session and runtime preserves `UpdatedAt`, so the sidebar's acknowledged
  Done badge does not reappear when Claude Code's `idle_prompt` follows Stop
  (`internal/agentactivity/store.go::Store.HandleEvent`).
- Claude Code's `idle_prompt` notification fires after a minute of waiting
  whatever the agent waits for. It maps to `done` only after a recorded
  `working` or `done` turn; a pending `input` or `approval`, a never-prompted
  `idle` session, or a missing report stays untouched, so it cannot make an
  unprompted agent look resumable to idle stop. Only `elicitation_dialog` and
  user-input tools put a session into `input`
  (`internal/agentactivity/store.go::Store.HandleEvent`).
- Only Stop refreshes pending background work and scheduled wakeups; every other event preserves it for the same conversation and runtime (`internal/agentactivity/store.go::Store.HandleEvent`).
- Each report's read, merge and write is serialized within the store, so a concurrent hook cannot overwrite Stop's pending work with a stale merge (`internal/agentactivity/store.go::Store.reportLock`).
- Reports are reconciled against persisted and live runtime session keys
  after startup restoration and after every missing-tmux prune, so a report
  whose runtime row was pruned does not outlive it
  (`internal/server/workspaceapi/lifecycle.go::Handler.reconcileAgentActivityReports`).

- Retry resumes setup in place and retains branch, worktree, and runtime records;
  restarting a terminal must never erase local commits or dirty files
  (`internal/workspace/manager.go::Manager.RequestRetry`).
- A saved branch may have advanced beyond its original provider head. Reattach it
  without resetting commits, and preserve it if later setup fails
  (`internal/workspace/manager.go::Manager.SetupWithOptions`).
- A clone's PR source branch alone does not prove workspace recovery. Missing
  checkouts retain their Git registration and index through failed recovery,
  preserving detached commits (`internal/workspace/manager.go::Manager.addWorktree`).
- An ad-hoc branch name alone is no proof of prior setup; recovery needs the
  existing checkout or its registration to distinguish late branch collisions
  (`internal/workspace/manager.go::Manager.addWorktree`).
- An empty managed branch means an adopted branch or detached HEAD; unknown
  means setup has not recorded ownership. Recover the former from its registered
  HEAD without taking branch ownership (`internal/workspace/manager.go::Manager.addWorktree`).
- Empty branch metadata does not prove checkout recovery. Roll back fresh
  failed checkouts while retaining adopted source branches
  (`internal/workspace/manager.go::Manager.SetupWithOptions`).

## Provider-Backed Lifecycle Facts

Provider-backed workspace execution is local, but its provider facts remain
hub-owned.

- Creation commits the workspace and its versioned launch specification in one
  transaction before starting asynchronous setup. The specification preserves
  the exact base clone, default branch, requested workspace branch, PR head
  kind, and fork clone URL selected at admission
  (`internal/db/queries_workspace_launch_specs.go::DB.CreateWorkspaceWithLaunchSpec`).
- Initial setup, retry, crash recovery, pull/push branch synchronization, PR
  association, and pushed-head monitoring pass through
  `internal/workspace/launch_spec.go::Manager.RequireWorkspaceLaunchSpec`.
  These paths must not reconstruct provider facts from repository, pull, or
  issue cache rows on a spoke.
- A source-visibility lease is valid only while `now < source_visible_until`.
  There is no grace period. An unexpired specification permits the lifecycle
  operation after local route and Git checks; expiry requires hub
  refresh before setup or provider-backed network Git. An unavailable
  hub yields a typed retryable `LaunchSpecRefreshError`, and an
  explicitly hidden source fails closed for those operations.
- Starting a new local shell or agent session does not contact the hub.
  Generated agent context uses the last validated launch specification and may
  therefore contain stale provider labels while the hub is unavailable.
- Same-repository PRs fetch the hub-issued synthetic pull ref from the
  base clone. Fork PRs fetch the exact persisted fork clone URL and head branch;
  a spoke never derives a fork URL from provider cache state. Issue workspaces
  start from the persisted base clone/default branch. Ad-hoc and Kata workspaces
  continue through their local resolution paths.

## Terminal Transport

- Every terminal WebSocket leg negotiates RFC 7692 `permessage-deflate` with
  context takeover; Fleet relays apply the same policy to their separately
  terminated upstream leg. (`internal/terminalwebsocket/`)
- Use standard negotiation, not an application compression envelope: context
  takeover shares a dictionary across PTY messages, while unsupported peers
  continue with an ordinary uncompressed WebSocket.
- The browser pane that claims resize authority owns the complete character and
  pixel geometry; hidden or non-owning panes cannot overwrite it. Retained PTYs use
  an 8x16 fallback only until claimed browser geometry arrives (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::resizeVisibleTerminal`).
- PTY resize must retain the file descriptor across its ioctls and preserve nonblocking
  I/O; terminal closure can overlap a resize (`internal/ptysize/resize_unix.go::Resize`).
- A hub terminates the browser WebSocket and opens a separately
  authenticated WebSocket to the owning spoke. It forwards bytes and resize
  messages only; the spoke still owns tmux/ptyowner state, persistence, exit
  observation, and cleanup (`internal/server/fleetapi/fleet_proxy.go`).

## Natural Exit Rules

Natural process exit should collapse stale runtime state quickly.

- When a launched runtime session exits naturally, remove it from backend
  runtime state and from the workspace UI.
- If the exited session was active, select a remaining session; empty workspaces
  show launch choices (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::selectFallbackTab`).
- If the session was tmux-backed, forget the persisted runtime tmux row once the
  backing tmux session is gone.
- When the shell drawer process exits, close or collapse the drawer, forget any
  persisted runtime tmux row once the backing tmux session is gone, and require
  a fresh launch on reopen.
- PTY-owner process exit may precede final output; keep subscribers alive for a
  bounded drain or loaded runners lose the last repaint
  (`internal/workspace/localruntime/manager.go::watchPtyOwner`).
- PTY output EOF may precede process wait; give wait a bounded chance to publish
  the real exit code without marking the owner fully exited before replay drains
  (`internal/ptyowner/owner.go::exitCodeAfterOutputClose`).
- The opt-in Rust PTY manager must keep the Go owner's exit-code, output-drain,
  post-exit linger, and stop semantics; change both owners together
  (`rust/pty-manager/src/main.rs::owner_should_exit`).
- Never close an owner connection without a response: `Client.Stop` and
  `Client.Ensure` read EOF as a dead owner and delete its state. The Rust
  manager leaves connections queued while at capacity (`rust/pty-manager/src/main.rs::run_owner`).

The base workspace `tmux` tab is the exception:

- Keep reconnect behavior for the base `tmux` tab.
- Do not auto-close that tab just because the websocket detached or the view
  remounted.

## Delete Ordering Rules

Workspace deletion is intentionally conservative.

- Interactive DELETE checks dirtiness before durable admission; queued cleanup admits
  first and records dirty refusal as `deletion_failed`
  (`internal/server/workspaceapi/workspace_deletion.go::Handler.runWorkspaceDeletion`).
- Persist deletion intent before destructive work; failures remain visible and
  retryable, while interrupted attempts become explicit failures after restart
  (`internal/server/workspaceapi/workspace_deletion.go::Handler.runWorkspaceDeletion`).
- Setup generations are process-local, so startup must change persisted
  `creating` rows to `error` and `deleting` rows to `deletion_failed`
  (`internal/server/workspaceapi/lifecycle.go::Handler.Start`).
- Lifecycle writers that act on a previously observed status must use a
  conditional transition. In particular, tmux pruning cannot replace an
  admitted `deleting` state with a stale `ready` to `error` write
  (`internal/db/queries.go::DB.MarkReadyWorkspaceError`).
- `BeginStopping` blocks new launches before preflight without stopping sessions;
  existing runtimes stop only after preflight passes or force skips it
  (`internal/server/workspaceapi/workspace_deletion.go::Handler.runWorkspaceDeletion`).
- Only after runtime shutdown succeeds should destructive worktree and DB
  teardown continue.
- Deletion clears activity reports and stop marks for stored runtimes, including
  parked sessions absent from the live manager inventory.
- A live worktree registration at the persisted path in the resolved repository
  authorizes cleanup without an ownership marker; a same-repository replacement
  is the workspace, but a symlink to another worktree is not.
  (`internal/workspace/manager.go::gitDirOwnsCleanupWorktree`)
- A confirmed delete publishes workspace absence from the application workflow before presenter-specific navigation
  or failure UI; releasing the initiating presenter must not suppress tombstones, hosted-session cleanup, or route invalidation
  (`frontend/src/lib/components/terminal/workspace-runtime-workflow.ts::executeMutation`).
- Workspace `DELETE` and merge-cleanup admission are idempotent after
  authoritative absence; stale projections and retries succeed without
  publishing another deletion
  (`internal/server/workspaceapi/workspace_deletion.go::Handler.QueueWorkspaceDeletion`).
- A merge that enqueues keeps its workspace deletion durably. One loop resolves kept
  deletions at startup, when sync records a merged or closed snapshot or a queue change,
  and periodically: merged deletes on the owning daemon and retries a refusal; closed, or
  a queue read newer than the request showing it unqueued, drops it
  (`internal/server/pullapi/workspace_cleanup.go::Handler.resolveMergeWorkspaceCleanups`).
  Accepted by the maintainer: a later enqueue without deletion does not cancel a kept
  request, a failed save is only logged without a warning to the user, and spoke
  preparation neither waits for nor hands off kept requests.

This ordering prevents a rejected delete from silently killing the user's live
workspace sessions.

## Server Shutdown Ordering

- Let spare-worktree registration finish within its own bounded context; canceling
  Git mid-write can corrupt metadata used by every worktree in the repository
  (`internal/workspace/hot_worktree.go::Manager.prepareHotWorktree`).

Workspace and Fleet own independent idempotent, context-bounded lifecycles;
Fleet starts after Workspace and shuts down its workers before Workspace stops
(`internal/server/fleetapi/handler.go::Handler.Shutdown`). Root closes
Pull admission and cancels its workers before HTTP drain, then waits for Pull before
Fleet in the post-drain dependency stage (`internal/server/server.go::Server.Shutdown`,
`internal/server/pullapi/handler.go::Handler.Stop`).
If any stage times out, shutdown must not advance; a later call resumes at the
blocked stage (`internal/server/streamapi/workspace_dependency_shutdown.go::WorkspaceDependencyShutdown`).

Federated terminal sessions stay spoke-owned. The hub bridges the
browser WebSocket directly to the enrolled spoke's HTTPS origin and does not
create a local process, PTY, or durable transport session
(`internal/server/fleetapi/fleet_proxy.go::Handler.serveFleetWebSocketProxy`).

## Tmux Persistence Rules

- Startup must attempt agent recovery before pruning missing runtime rows
  (`internal/server/workspaceapi/lifecycle.go::Handler.RestoreRuntimeSessions`).
- The runtime row, not the activity report, holds the conversation to resume:
  every non-SessionEnd hook whose canonical CWD is the row's workspace saves its
  agent and conversation ID on the row, so a nested agent in another worktree cannot
  replace it, and re-recording the row keeps them. A nested agent in the same worktree
  inherits the runtime key, so another conversation replaces the saved one only once
  the saved one has no report (SessionEnd, which /clear and /resume send first,
  removes it), or, with a report older than the row's runtime or this daemon, through a
  SessionStart: hooks are lost while no daemon listens, and a nested agent that outlived
  a restart reports before its waiting parent. A conversation that ended with reason
  "other" keeps the row until it reports again. Agents without SessionEnd hooks (Droid)
  save their newest conversation. A runtime's start is taken before its pane starts.
  Recording an agent's row also saves a conversation its hooks reported before the row
  existed, only into a row that saves none and only when one conversation reported.
  Hooks do not say which agent sent them, so a hooked nested agent in the same worktree
  can still become the saved conversation when its SessionStart beats its waiting
  parent's first hook after a restart, or when it reports first after an ambiguous
  early import (accepted limit; process lineage is the robust signal). SessionEnd may delete the report, including when a
  host shutdown kills the agent, and saves its reason on the row while that conversation
  is the saved one
  (`internal/server/workspaceapi/agent_hook.go::Handler.recordAgentConversation`,
  `internal/db/queries.go::DB.RecordWorkspaceRuntimeConversation`).
  Recovery and idle resume first copy the newest activity report into an agent row
  that has no saved conversation, for agents running before migration 000066; a failed
  write still resumes that attempt from the report. Remove that import
  once no running agent can predate it
  (`internal/server/workspaceapi/lifecycle.go::Handler.importReportedConversation`).
- Missing tmux agents, and ptyowner agents whose owner is gone (no state, or a
  socket that refuses or no longer exists, as after a reboot), resume the row's
  saved Codex, Claude, Pi, Copilot, Cursor, Droid, or Gemini conversation by exact ID, preserving configured flags and runtime identity without replaying
  the initial prompt. A gone
  ptyowner session with nothing to resume stays unavailable until the user stops it
  (`internal/server/workspaceapi/agent_resume.go::Handler.resumeWorkspaceAgent`,
  `internal/workspace/localruntime/ptyowner_runtime.go::attachPtyOwnerSession`).
- Agent recovery must enforce the same Git identity and credential-helper checks as a fresh
  launch; retain the conversation when validation fails so repair allows a later retry.
  (`internal/server/workspaceapi/agent_resume.go::Handler.resumeWorkspaceAgent`)
- Kit owns generated resume arguments; configured arguments pass through
  unchanged, with their meaning and validity owned by the caller
  (`internal/workspace/localruntime/agent_resume.go::agentResumeCommand`).
- Recovery never resets worktrees or resends the initial message; failed
  recovery retains the saved runtime row for another attempt
  (`internal/server/workspaceapi/lifecycle.go::Handler.RestoreRuntimeSessions`).
- Restore tmux base terminals before agents, including retained creating/error
  retries; startup recovery shares a 30-second budget and preserves uncompleted
  attempts (`internal/server/workspaceapi/agent_resume.go::Handler.restoreWorkspaceTerminals`).
  Pty-owner base terminals are never started by recovery; attach reuses a live
  owner or starts one (`internal/terminal/handler.go::Handler.ServeHTTP`).
  Activity enrichment reads an absent owner as idle without starting it; real owner errors still fail enrichment (`internal/server/workspaceapi/routes_handlers.go::Handler.probeOneTmuxSession`).
- A tmux client exits 0 whenever its session ends, even the server's last. Whatever
  the client's exit code, the pane wrapper's exit record decides: keep the agent when
  it shows a kill (the wrapper died with it, or it died from HUP, KILL, or TERM, as when
  a shutdown signals agents before tmux and Forge), and forget it after a recorded
  normal exit, which can follow a killed client, unless the saved conversation's
  SessionEnd gave reason "other": an agent can catch a termination signal and exit 0. Read the record once per decision.
  Each new wrapper clears the record a previous run under the same tmux name left;
  only deleting the agent's row spends a record, so an interrupted cleanup still
  shows the next start that the agent finished, and a detached client, which also
  exits 0 while the agent runs on, spends nothing. Startup recovery forgets, not resumes, a kept agent whose
  session is gone with a recorded normal exit, then prunes records of gone sessions
  that no stored row names, so an exit still being handled keeps its record. A runtime
  without tmux has no record, so its exit handler deletes the row at once rather than
  in background cleanup a shutdown can skip, and runtime shutdown waits (bounded) for
  exit handling already under way
  (`internal/workspace/localruntime/manager.go::Manager.Shutdown`); such an agent that finishes while Forge is
  down is still resumed, since its gone owner looks like a killed one. Sessions
  launched without a record keep the old meaning
  (`internal/workspace/localruntime/agent_exit.go`). An agent whose client exits
  non-zero lost its client or server unless tmux reports its session gone while the
  server it ran on still runs (a failed launch). Identify servers by pid and start time,
  read in the same tmux call that finds the session, so a replacement never counts as
  the session ending, and keep an agent whose server was never identified: keep its
  row and conversation pending for the next start, since a runtime never restores a key
  it saw exit. Post-launch cleanup and a finished reattachment apply the same rule to
  an exit that landed first (the exit handler skips a runtime while its recovery is
  pending), and all callers share one cached decision per runtime generation. Kept agents
  list as unavailable until Stop forgets them or the next start resumes them. Pruning
  deletes nothing while the server has no sessions, and judges each missing row only
  after listing tmux, never from an earlier snapshot: it keeps a pending runtime,
  leaves one whose exit this process has not seen to its exit handler (and, since that
  can mean a lost server, queues every base terminal for restore), and gives a seen
  exit the exit handler's decision, including a supervised chat's. It errors no
  workspace while base terminals are outstanding, rechecked after the rows are judged
  (`internal/server/workspaceapi/workspace_enrichment.go::Handler.retainMissingRuntime`).
  A periodic recovery pass that
  follows keeping such an agent restores every base terminal first, as startup does,
  even if the agent was stopped meanwhile, taking
  setup admission per workspace so a deletion is never undone. Workspaces a pass could
  not finish (busy, failed, or out of budget; terminals get only a share of each pass
  so a hung one cannot starve pending agents), including those startup recovery could
  not restore, or all of them while their list cannot be read, stay outstanding across
  passes, even after the kept agents are stopped; one startup restored only for its
  retained agents stays eligible after they resume, and a deleted one leaves the set.
  Later passes work only on those, each starting after the workspace the last pass
  reached, and the missing-tmux prune waits until none remain. Recovery changes must
  pass every shutdown order of `make test-reboot-sim`
  (`internal/server/workspaceapi/lifecycle.go::Handler.keepAgentAfterTmuxServerLoss`,
  `internal/server/workspaceapi/routes_handlers.go::Handler.forgetRecordedRuntimeSessionIfExited`,
  `internal/workspace/manager.go::Manager.PruneMissingTmuxSessions`).
- Pending startup recovery retries during periodic missing-tmux pruning under
  workspace setup admission; successful recovery restores ordinary exit cleanup
  (`internal/server/workspaceapi/lifecycle.go::Handler.RestoreRuntimeSessions`).
- Periodic recovery rotates one pending session per pass; a slow attempt must
  not starve later sessions (`internal/server/workspaceapi/lifecycle.go::Handler.restoreRuntimeSessions`).
- Explicit session stop shares recovery synchronization and forgets the saved
  row before releasing it (`internal/server/workspaceapi/routes_handlers.go::Handler.stopWorkspaceRuntimeSession`).
- Idle stop has one owner, `idleRuntimes` (`internal/server/workspaceapi/idle_runtime.go`).
  Every stop, resume and stop mark goes through it, except mark removal when a runtime or
  workspace is deleted or its tab is closed (`localruntime.Manager.ClearStopMark`).
  Callers only report a page view or typing, or ask whether a row is stopped.
- A pty-owner workspace's runtimes stop only after `workspaces.idle_runtime_stop_hours`
  with no page view and no typing. Idle time counts from the latest of last use,
  workspace creation and runtime creation.
- `Handler.idle` holds the owner only while the setting is on. Startup and one loop
  (`Handler.runIdle`, woken by `ApplyConfig`) switch it. With the setting off, nothing is
  recorded, saved or read.
- Turning the setting on starts every workspace's clock then. Turning it off, at runtime
  or startup, marks stopped agents and ACP chats recovery-pending, as after a reboot; a
  stopped shell reads as `error`. It also removes the stop marks and the saved clock
  (`localruntime.Manager.ClearIdleState`).
- While on, the 5-minute pass and shutdown save last use per workspace beside the stop
  marks (`localruntime.Manager.SaveIdleActivity`), and startup loads it, so a restart
  keeps idle time. The pass gives a workspace with none saved a last use of now.
- A page view is the showing page's `viewing=true` runtime read.
- Typing is terminal input beyond automatic replies, a submitted agent message, or an ACP
  prompt, permission or elicitation reply (`localruntime.Manager.SetInputObserver`). Binary
  input beyond automatic replies on the workspace terminal socket also counts
  (`terminal.Handler.Typed`). Socket opens, reconnects, focus, heartbeats, history and
  other reads are neither.
- Eligibility lives in `idleRuntimes.stoppable`. Command and tmux sessions never stop.
  These may stop:
  - Plain shells.
  - Hook agents with no working report under the runtime key, no pending background work
    or scheduled wakeups, and a resume path: `done`, `input` or `approval`, or `idle`
    after a SessionStart whose `source` is `resume` or `fork`
    (`agentactivity.Report.Continued`). The target must be available and its executable
    must run.
  - ACP chats with `loadSession`. Owners started by an older build record no
    `LoadSession` and lack `ACP.Park`, so they keep running per the no-compatibility rule
    above.
- Stopping writes the mark first and marks attachments as a recoverable detach. It stops
  ACP owners through `ACP.Park` and kills other owner trees, so the saved conversation and
  row stay. An owner that survives a failed stop is unmarked and reattached.
- The pass holds only the workspace's setup admission, with a bounded deadline per stop.
  Each stop reads the live timeout, so a reload affects a pass already underway.
- The pty-owner workspace terminal stops while idle only with no claimed socket; the stop
  claims its slot without holding the shared lock. It stays ready and starts a fresh shell
  on attach, without a stop mark (`internal/terminal/handler.go::Handler.StopUnattachedTerminal`).
- Only a page view resumes a marked row whose owner is gone (`ptyowner.Client.Gone`).
  Agents resume their conversation, shells start fresh under their key, and ACP chats
  reload.
- A viewing read that lists a `parked` row starts one background resume job per workspace
  (`idleRuntimes.ResumeStopped`) and returns at once; a later read lists the rows running.
  The job holds the workspace's setup admission throughout but `runtimeRestoreMu` only
  per row, so reads of other workspaces wait for at most one resume. Without admission it
  leaves the resume to the next view.
- A failed resume clears the mark and marks the row recovery-pending. The maintenance
  pass retries an agent, each read retries an ACP chat, and a shell reads as `error`.
- Sockets, other reads and MCP list a stopped row as `parked`. Startup recovery skips it
  and drops marks of forgotten rows.
- Removal of a created runtime backend is attempted when launch or persistence
  fails; it is best-effort, not retried, and a backend that survives a failed
  compensation is unrecorded until startup reaping. Reaping must protect stored
  workspace, host, and project-worktree sessions while recognizing every owned
  scope-derived tmux name (`internal/workspace/manager.go::ReapOrphanTmuxSessions`).
  Caller-keyed command ensure uses one manager transaction, and per-worktree shell
  discovery uses one handler transaction, through persistence and compensation so rollback authority cannot race adoption (`internal/workspace/localruntime/command_session.go::EnsureCommandSessionAndPersist`, `internal/server/workspaceapi/projects_handlers.go::Handler.ensureProjectWorktreeRuntimeShell`).
- Persistence failures stay client-visible; compensation failures are logged.
  Ptyowner rollback needs a live owned entry, and tmux fallback cleanup must
  match the launch marker, which adoption rewrites so a stale creator rollback
  cannot kill an adopted backend (`internal/workspace/localruntime/manager.go::RollbackLaunch`).
- A runtime can exit while its metadata write is still in flight, so every
  launch path must reconcile after persistence: live means both key and
  `CreatedAt` match, and a replacement must not suppress deletion of the exited
  generation's row (`internal/server/hostapi/host_runtime_handlers.go::Handlers.ForgetHostRuntimeCommandSessionIfExited`, `internal/server/workspaceapi/projects_handlers.go::Handler.forgetProjectWorktreeRuntimeSessionIfExited`).
- During kenn-forge shutdown, detach/restart behavior is different: do not treat
  normal server shutdown as a natural user exit that should erase recoverable
  base runtime state.
- Daemon shutdown keeps terminal tabs available for reconnection, including an
  intentional stop followed by a later start
  (`internal/workspace/localruntime/manager.go::Manager.Shutdown`).
- `KENN_FORGE_DEV_RESTART` remains an opt-out: unset, empty, or `1` enables
  restart-detach; other values disable it, except on execution workers
  (`cmd/kenn-forge/main.go::detachRuntimeSessionsForRestart`).
- Genuine exits and explicit session stops still close their tabs
  (`internal/server/workspaceapi/workspace_runtime_terminal.go::bridgeRuntimeAttachment`).
- Supervisors must leave durable tmux and ptyowner processes alive for
  reconnection; Forge-managed systemd units use `KillMode=process`
  (`internal/fleetsetup/service.go::renderSystemdUserService`).
- New tmux sessions use the `forge-` prefix; persisted `middleman-` session
  names remain valid and must not be rewritten (`internal/workspace/`).
- Check a tmux session's existence with an exact `=name` target: a runtime session's
  name extends its workspace's base name, and tmux otherwise accepts that prefix match
  for a missing base (`internal/workspace/manager.go::Manager.tmuxSessionExists`).
- Only `DefaultTmuxCommand` selects Forge's dedicated tmux server. Custom tmux
  commands may address a shared user server, so Forge never changes their global
  options or applies graphics-off mutations (`internal/config/config.go::IsDefaultTmuxCommand`).
- Dedicated tmux servers enable application OSC 52 forwarding at startup and
  session creation/reattachment; `external` drops agent copies before the browser
  can handle them (`internal/workspace/manager.go::ApplyTmuxClipboard`).
- The tmux server permanently retains its spawn environment for every pane to
  read via `show-environment -g`, and only `new-session` clients spawn it, so
  every Forge-issued tmux client runs with the non-secret allowlist
  environment, minus configured token names that could hide under the admitted
  `LC_`/`XDG_` prefixes — deny-by-default; never a bare strip list, which
  silently leaks any credential the configuration did not name. Pane processes
  (including the base workspace terminal, which keeps the credential-sanitized
  full daemon environment) get their environment only through the env-file
  handoff (`internal/workspace/localruntime/manager.go::TmuxClientEnvironment`,
  `internal/workspace/localruntime/tmux_launcher.go::NewTmuxPaneHandoff`).
- Keep terminal handoff files in Forge state storage: shared temporary-file
  exhaustion must not prevent writing the pane environment or script
  (`internal/workspace/localruntime/tmux_launcher.go::paneHandoffCommand`).
- The allowlist names exact variables only — never prefixes, which a secret
  such as `XDG_API_TOKEN` could hide under. It must keep `TMUX_TMPDIR`: tmux
  resolves `-L` sockets beneath it, so dropping it routes tmux clients to a
  different server than the manager owns. Attach commands returned to external
  callers always wrap in `env`, unsetting the caller's `TMUX` and setting or
  unsetting `TMUX_TMPDIR` to match the daemon. Fake tmux test fixtures bake
  their control paths into the script instead of smuggling them through the
  client environment
  (`internal/config/config.go::IsTmuxNonSecretEnvVar`).
- The non-secret terminal environment and credential names are disjoint by
  construction: config and Kata catalog validation reject `token_env` names
  that appear in the allowlist, because the tmux server's retained spawn
  environment can never be scrubbed after a name turns secret
  (`internal/config/config.go::Config.validateTokenEnvNamesNotTerminalVars`).
- Kata catalog `token_env` names feed every credential strip set on catalog
  load and at boot; catalogs load lazily per request, so the boot feed keeps
  earlier terminals covered (`internal/server/streamapi/server.go::Handlers.UpdateCatalogStripEnvVars`).
- Every attach command passes `-E`: a pane can widen the server's
  update-environment, and without `-E` the next attach copies the attach
  client's variables into the session environment; external attach specs
  additionally run in the caller's unsanitized shell
  (`internal/workspace/localruntime/tmux_launcher.go::tmuxAttachSessionCommand`).
- Every tmux client attach must force UTF-8; service launchers may omit locale
  variables, causing tmux to replace non-ASCII output before WebSocket transport
  (`internal/workspace/localruntime/tmux_launcher.go::tmuxAttachSessionCommand`).
- On macOS, tmux and PTY-owner shells with no configured `LANG`, `LC_ALL`, or `LC_CTYPE` must
  default `LC_CTYPE` to UTF-8 without overriding an explicit locale; launchd can
  otherwise leave zsh with incorrect Unicode prompt widths (`internal/workspace/localruntime/shell_environment.go::shellCharacterLocaleDefault`).
- Resolve the base terminal's login shell from the user database on every launch
  (macOS has no `getent`; use `id -P`) and skip a shell that is no longer executable.
  The daemon's `$SHELL` is fixed at its start, and a shell a system profile switch
  removed makes every new base pane exit at once, which the missing-tmux prune then
  reports as a lost session (`internal/workspace/manager.go::userLoginShell`).
- Forge's dedicated tmux server owns global passthrough, SIXEL, and mouse mode;
  live changes clear pane overrides, while custom servers receive passthrough
  only on Forge-owned panes and only while graphics are enabled
  (`internal/workspace/manager.go::Manager.ApplyTmuxGraphics`).
- Startup applies saved graphics and mouse settings before restoring retained
  clients on an existing dedicated tmux server
  (`internal/server/server.go::newServer`).
- Live graphics or mouse changes are best-effort; graphics updates try every
  managed pane before reporting combined failures, and setup reapplies the
  pane state
  (`internal/server/settingsapi/settings_handlers.go::Handlers.ApplyTmuxGraphics`).
- Enabling graphics installs each replacement retained client before detaching
  the old one; browser reconnects must always find an attachment while panes
  stay running
  (`internal/workspace/localruntime/manager.go::Manager.ReattachTmuxClients`).
- Terminal graphics are optional and default on. The browser supports SIXEL and
  iTerm images; Kitty graphics remain disabled while the xterm add-on labels
  that protocol alpha (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::syncImageAddon`).
- Retained tmux attach PTYs must start with nonzero pixel geometry: tmux asks for
  pixels before a browser subscribes and otherwise never retries the unanswered
  query needed for native SIXEL (`internal/workspace/localruntime/tmux_runtime.go::startTmuxAttachSession`).
- Local-runtime reconnects restore browser-generated cursor-key, mouse, focus,
  and paste DEC modes from session-wide PTY state, not bounded screen replay
  (`internal/workspace/localruntime/manager.go::session.subscribe`).
- Pass initial prompts to directly configured Claude commands as CLI arguments; its
  trust dialog already uses raw input, so a synthetic Enter can select exit.
  (`internal/workspace/localruntime/manager.go::Manager.LaunchWithInitialMessage`).
- Reject oversized Windows launch commands before starting the agent or PTY helper;
  count quoted UTF-16 arguments, including helper JSON, rather than prompt bytes
  (`internal/ptyowner/client.go::Client.Ensure`).
- Terminal prompt delivery requires observed bracketed-paste mode, then sends the
  opening frame, prompt, and closing frame in one terminal write and, after a
  short fixed settle delay, Enter as a separate write. Agent TUIs treat bytes
  in the same chunk as the paste-end marker as part of the paste, so an Enter
  in that chunk leaves the prompt unsubmitted. The paste-delay-Enter sequence
  holds the session input lock that attachment writes also take, and it runs
  to completion even when the caller's deadline expires first. Do not gate
  Enter on terminal echo: Claude renders pasted input only after the Enter
  event (`internal/workspace/localruntime/manager.go::session.submitInitialMessage`).
- Tmux advertises bracketed paste before its agent starts; require the pane's
  terminal to disable canonical input and echo before sending a prompt, or startup can discard it
  (`internal/workspace/localruntime/tmux_runtime.go::Manager.tmuxAgentInputReady`).
- Mode transitions precede one session-wide UTF-8-aware VT tail even in the
  alternate screen; retain split-rune introducers and decoded C1 controls/ST
  (`internal/workspace/localruntime/terminal_sequence_tail.go::trailingIncompleteTerminalDataLen`).
- Mode replay must mirror xterm.js 6 effective semantics: DEC private save/restore
  and ignored 1005/1015 encodings cannot re-enable modes or displace 1006/1016
  (`internal/workspace/localruntime/terminal_input_modes.go::terminalInputModeState.observe`).
- Recognize C1 CSI only as decoded U+009B; valid non-C1 UTF-8 invalidates pending
  controls, while invalid scalars, BOM, and standalone continuation bytes are
  decoder-discarded without reaching the VT parser
  (`internal/workspace/localruntime/terminal_input_modes.go::terminalInputModeState.observe`).
- Send reconnect cancellation to xterm as bytes, not text; only binary input
  clears its streaming UTF-8 decoder before replay
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::cancelPendingTerminalSequence`).
- Direct local-runtime sessions request a subscriber-only replay boundary and
  withhold resize/refresh controls until xterm parses it; the backend queues the
  boundary only after the retained VT/UTF-8 tail reaches ground. Fresh legacy
  and Fleet attachments still receive dimensions on every connection
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::connect`,
  `internal/workspace/localruntime/manager.go::session.subscribeInternal`).
- In xterm.js 6, DECSTR resets cursor keys, focus reporting, and bracketed paste,
  but leaves the mouse service's protocol and encoding unchanged
  (`internal/workspace/localruntime/terminal_input_modes.go::terminalInputModeState.softReset`).

## UI Contract Rules

The workspace UI should reflect runtime truth without leaving users stranded in
stale tabs.

- Runtime lists returned by `/workspaces/{id}/runtime` are the authoritative
  backend view of live launched sessions.
- Queued launch admission must read runtime after workspace readiness; that same fresh response may supply the reconciliation baseline, but presentation caches may not
  (`frontend/src/lib/components/terminal/workspace-runtime-workflow.ts::executeMutation`).
- Once a local ready event advances setup, older `creating` details must not hide the terminal or stall queued launch;
  fresh setup reads and authoritative error/deletion responses still apply
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::fetchWorkspaceProgram`).
- Manual stop settlement must bound every awaited stage and publish confirmed local absence before any
  best-effort refresh; no stalled transport, authority read, or presenter may retain the pending control
  (`frontend/src/lib/components/terminal/workspace-runtime-workflow.ts::makeWorkspaceRuntimeWorkflow`).
- Publish confirmed workflow and terminal sessions before best-effort reloads, but keep those additions provisional: only a successful
  runtime read may tombstone absent peers. Reject only definite failures; reconciliation decides uncertain launches
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::presentRuntimeMutation`).
- Workspace terminals use xterm.js exclusively; there is no renderer setting
  or alternate renderer path (`frontend/src/lib/components/terminal/TerminalPane.svelte`).
- Terminal clients must replace silent half-open sockets without a page reload; every local terminal backend must answer the shared round-trip heartbeat, and Fleet bridges must pass it through so idle output is not required for recovery
  (`frontend/src/lib/components/terminal/terminal-session.ts::makeTerminalSessionController`,
  `internal/terminalwebsocket/terminalwebsocket.go::WriteHeartbeat`).
- Own every non-empty browser text paste at the terminal container boundary,
  sanitize and send it once, and do not delegate single-line paste to xterm;
  this event path must remain usable on insecure HTTP origins without the async
  Clipboard API (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::handleTerminalPaste`).
- Browser text wins over image data; image-only paste uploads to the selected
  terminal host and pastes returned paths instead of reading that host's clipboard
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::uploadAndPasteImages`).
- Treat terminal processes as native-terminal-equivalent, but accept bounded, write-only OSC 52 writes only after one
  recent one-shot trusted DOM gesture; terminal data callbacks are not input provenance, and browser denial falls back
  through CSRF-protected loopback (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::handleTerminalKeyDown`).
- Keep OSC 52 validation synchronous and ahead of gesture consumption; replacing it with `@xterm/addon-clipboard`
  would require custom prefilters, nonblocking write separation, and handler-order coupling to preserve current
  rejection, parser-progress, and read-denial guarantees.
- Terminal clipboard authority is revoked on external pointerdown/focus transfer, pane inactivity/parking, disablement,
  or window focus/visibility loss; pointerdown capture closes the race before a competing browser copy starts, and
  revocation also stops in-flight browser-to-loopback fallback chains before their next stage
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::handleDocumentPointerDown`).
- Terminal pointerdown only prepares clipboard access; selection intent requires both a four-CSS-pixel dead zone and
  movement of roughly one rendered terminal cell, so physical focus-click jitter expires without reaching browser or
  loopback clipboard fallbacks at readable terminal geometry without suppressing deliberate drags.
  Confirmed captured drags retain authority only through internal or destinationless focus movement while the pane stays
  active; their watchdog also releases browser capture, so a missing release cannot shield later focus loss
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::hasPointerSelectionIntent`).
- Host clipboard writes require a local browser; trusted loopback proxies must report exactly one client IP assigned
  to the host, because the proxy's loopback `RemoteAddr` alone does not establish browser locality
  (`internal/server/authapi/terminal_clipboard_access.go::IsLocalTerminalClipboardRequest`).
- During active tmux SGR drags outside xterm bounds, add only clamped edge wheel, drag, and release reports; forward
  all other mouse reports unchanged, and never retain unsent drag state across a WebSocket boundary
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::connect`).
- Agent TUIs in the normal buffer with no local scrollback receive vertical wheel gestures as cursor input; xterm/tmux
  keep ownership when scrollback, the alternate buffer, mouse tracking, or browser Ctrl-wheel zoom is active
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::handleTerminalWheel`).
- Touch-fling momentum must never reach mouse-tracking apps as unpositioned wheel reports: xterm's post-lift
  `-xterm-gesturechange` events omit `clientX`/`clientY`, so the pane pins them to the last finger position in a
  capture listener before xterm encodes `ESC[<65;NaN;NaNM`
  (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::handleTerminalGestureChange`).
- macOS loopback clipboard fallback must run `pbcopy` with `LC_ALL=en_US.UTF-8`; service launchers may omit
  a UTF-8 locale and make `pbcopy` reinterpret unchanged UTF-8 input
  (`internal/systemclipboard/systemclipboard.go::nativeWriter.WriteText`).
- Windows loopback clipboard fallback must send UTF-16LE to `clip.exe`; UTF-8 stdin is code-page-dependent and corrupts
  non-ASCII text (`internal/systemclipboard/systemclipboard.go::encodeUTF16LE`).
- The frontend may react immediately to terminal exit events, but should then
  reconcile with a runtime refresh.
- Only the active terminal pane may publish cell geometry; font-metric changes
  must refit and publish columns/rows through an ownership-aware geometry control because unchanged
  container pixels do not trigger resize observation (`frontend/src/lib/components/terminal/XtermTerminalPane.svelte::resizeVisibleTerminal`).
- Keyboard and pointer interactions inside workspace rows must not trigger
  unintended navigation when the user is targeting a nested control.
- Selected tabs persist on the workspace's execution host and restore across browsers; browser storage is only a cache.
  Restoration and automatic focus must not overwrite a saved choice; promoted agent focus counts as selection
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::loadWorkspaceTabProgram`).
- Promoted terminal-region sessions remember `terminal` without changing detail-pane placement; open its top dock
  on entering Workspaces. Resolve saved session keys against the browser's current region
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::restoreWorkspaceTabSelection`).
- Order selection requests per workspace and execution host; a delayed peer must not block another workspace
  (`frontend/src/lib/components/terminal/workspace-view-state.ts::workspaceRequests`).

## Released Terminal Retention

- Workspace presentation and terminal retention have separate limits; a retained socket alone cannot restore an evicted workspace view. Size detail and workspace caches independently of connected terminal retention
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::recentWorkspaces`).
- Repeat visits restore host-scoped workspace/runtime presentation before revalidation; cached runtime may reclaim retained sessions but cannot decide queued launches, authorize new attachments, or discard absent peers
  (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::restoredSessionKeys`).
- Event-stream `Open` can arrive after initial loading; it is not runtime invalidation. Only pending workspace enrichment
  needs a detail refresh after the initial load, while `ReconnectStale` still reloads workspace and runtime
  (`frontend/src/lib/components/mobile/MobileWorkspaceTerminal.svelte`).
- Parked workspace hosts stop periodic runtime reads unless a promoted pane remains visible; this must not stop
  application-owned launch reconciliation (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::startRuntimePolling`).
- A live view's desired set claims its sessions even when a tab is hidden; only unclaimed sessions enter the bounded, release-ordered LRU, and a zero limit disables retention (`frontend/src/lib/stores/session-host.svelte.ts::noteSessionReleased`).
- While a workspace switch awaits destination runtime reconciliation, cache trimming must protect that destination prefix; otherwise releasing the previous workspace can evict the pending cache hit at capacity (`frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte::releaseOwnedSessions`).
- Retention keeps the parsed xterm subtree and connected socket but relinquishes interaction, resize, and WebGL resources; reclaim reparents the same subtree without reconnect or replay (`frontend/src/lib/components/terminal/PooledSessionTerminal.svelte`, `frontend/src/lib/components/terminal/XtermTerminalPane.svelte::syncRendererState`).
- Released sessions are browser presentation cache, never runtime authority: disconnect, exit, deletion, or stale generation causes app-level final discard rather than background reconnect (`frontend/src/lib/stores/session-host.svelte.ts::noteSessionExited`, `frontend/src/lib/stores/session-host.svelte.ts::discardSessionsWithPrefix`).

## Shell Command Override

When tmux is unavailable, the plain shell session is launched through ptyowner
rather than as a direct child of kenn-forge. This decouples shell ownership and
lifetime from the kenn-forge server process. Hardened deployments (systemd
services with `SystemCallFilter=~@privileged`, `LockPersonality=`,
`MemoryDenyWriteExecute=`, etc.) can still need a `[shell] command` wrapper or
external ptyowner manager path that starts the shell outside the restricted
service unit: zsh and bash both call `setresuid(uid, uid, uid)` during startup to
drop saved-uid privileges, and that syscall is in `@privileged`.

For these deployments, set `[shell] command = [...]` to wrap the launch
in something that escapes the parent unit's filter. On systemd hosts,
`systemd-run --user` spawns a fresh transient unit with its own
(unfiltered) policy:

```toml
[shell]
command = [
  "systemd-run", "--user", "--quiet", "--collect", "--wait", "--pipe",
  "--service-type=exec",
  "--property=KillMode=process",
  "--description=kenn-forge shell",
  "--",
  "zsh",  # absolute path or PATH-resolvable name; see below
]
```

Notes:

- `cwd` is propagated by the runtime via `cmd.Dir` — your wrapper must
  forward it to the actual shell. With `systemd-run`, that's
  `--working-directory=$PWD` (or a fixed path); without an explicit
  flag the transient unit does not inherit the launcher's working
  directory.
- The configured argv is invoked verbatim (no shell expansion). The
  first element must be an absolute path or a `PATH`-resolvable name;
  relative paths are rejected so a malicious worktree cannot drop a
  binary into itself and gain code execution.
- When unset, the runtime falls back to `$SHELL`, then `/bin/sh`. This
  is the safe default for unhardened single-user installs.

The `[tmux] command` setting follows the same wrap-it-in-systemd-run
pattern for similar reasons; the two are independent.

## Switch-Timing Instrumentation

The frontend emits one-shot `workspace-switch:<phase>` User Timing measures per
workspace switch (route selection through terminal first paint), recorded via
`frontend/src/lib/instrumentation/workspaceSwitchTiming.ts`. The phase names
are stable API for before/after performance comparisons — do not rename them,
and record new phases through that module so superseded-switch and duplicate
guards keep applying. `make profile-workspace-switch` captures a reproducible
profile; see `frontend/tests/profiling/README.md`. Each measure's `detail.traceId`
joins it to the same request's server-side OTel trace, whose export is opt-in
via `OTEL_TRACES_EXPORTER`.

- Every frontend HTTP path, including hand-written runtime requests, must use
  the shared traced fetch boundary so `traceparent` and `baggage` are not lost
  when code bypasses the generated client (`frontend/src/lib/api/runtime.ts::tracedFetch`).
- Base-path routing must preserve the inner Huma route pattern for outer OTel
  middleware; otherwise prefixed API spans collapse to the base-path pattern
  (`internal/server/otelmiddleware/otel_middleware.go::StripPrefixPreservingPattern`).
- A workspace-switch trace ends at terminal first paint or after 30 seconds;
  cancellation and supersession must clear the matching fallback timer
  (`frontend/src/lib/instrumentation/workspaceSwitchTiming.ts::endSwitchTrace`).
- Automatic HTTP tracing excludes only exact long-lived stream routes/modes;
  short endpoints such as telemetry event capture remain traced
  (`internal/server/otel_middleware.go::otelTraceable`).
- Federated terminal WebSockets need their own bounded attach span, ending
  after setup and before the long-lived spoke-to-browser bridge
  (`internal/server/fleetapi/fleet_proxy.go::startFleetAttachSpan`).

## Testing Expectations

Prefer full-stack coverage when the bug crosses backend lifecycle and frontend
behavior.

- Use real SQLite-backed server tests for delete ordering, tmux cleanup, and
  runtime-session API behavior.
- Workspace/Projects handler and Git-heavy wire tests belong to
  `internal/server/workspaceapi` or `internal/server/workspacetest`; Git and
  worktree cases in the public wire lane must acquire its weighted semaphore.
- Root-retained Git tests must cross a root composition boundary and acquire
  the root Git semaphore before expensive setup; `t.Parallel` alone is never
  a Git-work concurrency bound (`internal/server/api_test.go::acquireRootWorkspaceGitSlot`).
- A server test that creates a workspace must wait for setup to reach a terminal
  state (`waitForWorkspaceReady`) before it returns. The `202 Accepted` create
  runs clone/setup in a background goroutine; if the test returns first, that
  goroutine can keep writing into the test's `t.TempDir` clone path and race
  `RemoveAll` teardown, failing intermittently with "directory not empty".
- Workspace `ready` can precede the post-setup pushed-head observer pass. Tests
  that mutate upstream state after readiness must use a state that observer
  cannot repair (`internal/server/workspaceapi/routes_handlers.go::Handler.runWorkspaceSetupWithBasePath`).
- Spoke lifecycle tests for provider-backed workspaces seed a launch specification
  and leave provider-item tables empty. Lease-expiry tests use the handler clock
  so admission, setup, and HTTP error mapping observe the same instant.
- Kata API fixtures must use their package-private tmux server and force-delete
  created workspaces; shutdown preserves durable base sessions, so temp-dir
  cleanup alone leaks (`internal/server/kata/testmain_test.go::TestMain`).
- Use tmux wrappers/fakes for missing-session and dead-server cases.
- Tests exercising the unconfigured tmux default must sandbox `TMUX_TMPDIR`
  and probe the `kenn-forge` socket name literally; deriving the probe from
  the default command would follow a regressed default and pass
  (`internal/server/workspacetest/default_tmux_socket_test.go`).
- Add frontend or Playwright coverage when the regression is visible in tab
  selection, shell drawer state, or workspace navigation.
- Federation terminal tests must cross an authenticated TLS boundary; a
  handler-only test cannot prove credential forwarding, WebSocket proxying, or
  spoke-owned runtime behavior.

Related intent docs:

- [`context/workspace-apis.md`](./workspace-apis.md) for workspace API scope and
  non-goals.
- [`context/ui-interaction-contracts.md`](./ui-interaction-contracts.md) for
  row/button, tab, and keyboard interaction expectations in the UI.
