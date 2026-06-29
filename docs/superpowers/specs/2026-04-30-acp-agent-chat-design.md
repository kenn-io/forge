# ACP Agent Chat Design

## Goal

Add a richer agent chat experience to workspaces mode by integrating ACP, the Agent Client Protocol used by editors and web applications for coding-agent conversations, while keeping the chat surface detachable so it can later run as an ambient floating sidebar outside a workspace.

## Scope

This design covers a first ACP-backed chat integration for middleman-owned workspaces and the architectural seams required for future ambient sessions.

The first slice supports:

- Configured ACP agent commands launched by the middleman server.
- Workspace-scoped agent sessions rooted at the workspace worktree path.
- A chat UI that renders user messages, assistant message chunks, plans, tool calls, permission prompts, errors, and cancellation state.
- A detachable frontend chat surface that can be mounted inside workspaces mode or, later, inside a floating ambient sidebar.
- Backend session state that allows `workspace_id` to be absent so ambient sessions can reuse the same session manager and UI contract.

The first slice does not replace the current runtime terminal tabs. Terminal-backed agent sessions remain available for raw TTY workflows, while ACP chat becomes the structured conversation path.

## Success Criteria

The first workspace-focused implementation is successful when:

- A configured ACP agent can be discovered, initialized, and shown as available or disabled with a concrete reason.
- A user can open agent chat from a ready workspace and create a session rooted at that workspace worktree.
- Workspace chat derives `cwd` server-side from `workspace_id`; browser input cannot move a workspace session outside the worktree.
- A user prompt streams assistant text, plan updates, tool calls, and errors into the thread without needing a raw terminal tab.
- User and assistant chat text renders as sanitized GitHub Flavored Markdown, including table support.
- Successive tool calls are grouped by default so busy agent activity stays scannable.
- ACP plan/task-list updates are captured as durable process indicators that can be rendered outside the chat transcript.
- The composer supports `@` file autocomplete scoped to the session root and `/` autocomplete for ACP slash commands plus configured middleman skills.
- Transcript events persist and reload after browser refresh or workspace navigation.
- Pending permission prompts persist and can be resolved after browser refresh.
- Session titles, metadata, available commands, and configuration options update from ACP session setup responses or notifications without browser polling.
- Cancelling an active turn sends `session/cancel` and leaves the session in a clear idle, cancelled, or errored state; closing a session uses `session/close` when the agent advertises it.
- Filesystem access outside the allowed root is rejected and covered by tests.
- Existing tmux, shell, and terminal-backed runtime sessions continue to work unchanged.
- Full-stack e2e coverage exercises the HTTP API, SQLite persistence, fake ACP agent, and event stream.

## ACP Protocol Model

Middleman acts as an ACP client. The server launches an ACP-compatible agent process on demand and speaks UTF-8 JSON-RPC over the process stdin/stdout transport. The write path sends newline-delimited JSON-RPC as ACP specifies. The read path should accept newline-delimited messages and complete JSON values that arrive without a trailing newline; `codex-acp` 0.16.0 accepts newline-delimited input but returns initialize responses without a newline terminator in local validation. Agent stderr is captured for diagnostics and never parsed as ACP protocol data.

Each agent connection starts with `initialize`, where middleman advertises client capabilities such as filesystem access and terminal support. The agent response supplies protocol version, agent metadata, supported prompt capabilities, top-level `loadSession` support, session capabilities such as list, resume, close, delete, and additional directories, MCP transport capabilities, and authentication methods.

Workspace chat creates an ACP session with `session/new` using:

- `cwd`: the workspace `worktree_path`.
- `mcpServers`: an empty list in the first slice.

The first workspace slice does not request additional roots. Middleman only sends `additionalDirectories` when the agent advertises `sessionCapabilities.additionalDirectories`; when it does send the field for new, loaded, or resumed sessions, the value is the complete intended additional-root list rather than a patch.

The browser never owns workspace `cwd`. For workspace sessions, `workspace_id` is the authority and the server resolves `cwd` from the persisted ready workspace. If a request body includes `cwd` with `workspace_id`, the server rejects the request unless it exactly matches the canonical workspace worktree path after cleaning symlinks and path separators.

Ambient sessions may include `cwd`, but only when it is absent or contained within an explicitly configured ambient allowed root. If an ambient `cwd` is outside the allowed root set, session creation fails before launching an agent or advertising filesystem capability.

Middleman has two layers of session history. Middleman-native history is the persisted `acp_sessions` and `acp_events` tables used by the browser. ACP agent-native history is available only when the agent advertises `sessionCapabilities.list`, legacy `loadSession`, `sessionCapabilities.resume`, or `sessionCapabilities.delete`. The first slice should not depend on agent-native history, but the design keeps a narrow adapter so later work can call `session/list` for external history discovery, `session/load` when full replay is wanted, `session/resume` when middleman already has the transcript, and `session/delete` only for an explicit user history-removal action.

Session-level configuration and slash commands are discovered after session setup, not through a dedicated initialize capability. Agents may return initial `configOptions` from `session/new`, `session/load`, or `session/resume`, and may later send `config_option_update` notifications with the full current option list. Agents may send `available_commands_update` notifications at any point after session creation. Local validation against `codex-acp` 0.16.0 showed `session/new` returning model and reasoning-effort config options, while initialize advertised no separate config-option or slash-command capability.

User turns use `session/prompt` with text blocks and optional resource blocks. During a turn, the agent streams `session/update` notifications for user message replay, assistant message chunks, plans, thoughts, tool calls, session info updates, available slash commands, config-option updates, and mode changes. Cancellation uses `session/cancel` for the active turn. Session teardown uses `session/close` when supported so the agent can cancel in-flight work and release resources without middleman killing the whole ACP process.

Plan updates are especially important. ACP sends task-list style updates with `sessionUpdate: "plan"` and `entries` containing `content`, `priority`, and `status`. Middleman treats these as structured process state, not prose. The latest plan for a session is usable as an activity/progress indicator in workspace mode, ambient sidebar mode, tab chrome, and future session lists.

Relevant ACP documentation:

- https://agentclientprotocol.com/protocol/v1/transports
- https://agentclientprotocol.com/protocol/v1/initialization
- https://agentclientprotocol.com/protocol/v1/session-setup
- https://agentclientprotocol.com/protocol/v1/session-list
- https://agentclientprotocol.com/protocol/v1/session-delete
- https://agentclientprotocol.com/protocol/v1/prompt-turn
- https://agentclientprotocol.com/protocol/v1/tool-calls
- https://agentclientprotocol.com/protocol/v1/session-config-options
- https://agentclientprotocol.com/protocol/v1/slash-commands

## Backend Architecture

Add an `internal/acp` package with three focused responsibilities.

`transport` owns one launched agent process. It starts the configured command, sends JSON-RPC requests, maps response IDs back to callers, routes notifications, handles agent-to-client requests, records stderr diagnostics, and shuts down the process when no sessions need it.

`manager` owns middleman ACP sessions. It creates sessions, binds them to a transport, stores live status, appends normalized transcript events, broadcasts updates to subscribers, and exposes prompt, cancel, close, permission, and configuration operations to HTTP handlers. A session has a middleman session ID, optional workspace ID, selected agent key, ACP agent session ID, cwd, status, title, agent metadata, timestamps, and last error.

`clientcaps` implements the client-side ACP methods middleman chooses to advertise. Filesystem reads and writes are scoped to allowed roots. For workspace sessions, the only allowed root is the workspace worktree path. For ambient sessions, allowed roots must come from explicit server configuration or a future user-selected root; an ambient session with no allowed root advertises no write capability.

Allowed-root checks happen on canonical absolute paths after resolving symlinks for existing path components. Reads require the target to be inside an allowed root. Writes require the parent directory to be inside an allowed root and reject path traversal that escapes that root.

Terminal callbacks use short-lived, non-interactive command execution in the session cwd for the first slice. They do not attach to the existing browser terminal pane. Long-running interactive terminals remain the job of the existing local runtime. This keeps ACP terminal callbacks predictable and avoids mixing structured chat events with raw PTY streams.

Permission requests from the agent are normalized into pending permission events and sent to the UI. The request's `toolCall` payload follows the same patch/upsert shape as tool-call updates, so the manager must merge it into the current tool-call state before rendering or persisting the permission prompt. The manager pauses the JSON-RPC response until the user selects one of the ACP-provided options or cancels the turn.

## Server API

Expose ACP through middleman-native REST and streaming APIs rather than exposing raw JSON-RPC directly to the browser.

New routes:

- `GET /api/v1/acp/agents`: list configured ACP agents, availability, labels, and disabled reasons.
- `POST /api/v1/acp/sessions`: create a session. Body includes `agent_key`, optional `workspace_id`, ambient-only optional `cwd`, and optional initial prompt.
- `GET /api/v1/acp/sessions`: list recent sessions, filterable by workspace ID.
- `GET /api/v1/acp/sessions/{id}`: return session metadata and transcript.
- `POST /api/v1/acp/sessions/{id}/prompt`: send a user prompt.
- `POST /api/v1/acp/sessions/{id}/cancel`: cancel the active prompt turn.
- `POST /api/v1/acp/sessions/{id}/close`: close the active ACP session and release agent resources when supported.
- `POST /api/v1/acp/sessions/{id}/permissions/{request_id}`: resolve a pending ACP permission request.
- `POST /api/v1/acp/sessions/{id}/config-options/{config_id}`: set a session configuration option and return the complete current option list.
- `GET /api/v1/acp/sessions/{id}/events`: stream normalized session events.
- `GET /api/v1/acp/context/files`: search mentionable files for a workspace or ambient root. Query parameters include `workspace_id`, ambient-only `cwd`, and `q`.
- `GET /api/v1/acp/skills`: list middleman-configured skills available to the selected agent profile. Query parameters include optional `agent_key` and `q`.
- `GET /api/v1/acp/sessions/{id}/commands`: list the latest ACP-advertised slash commands for the session.

Use SSE for the first browser streaming path because the app already has SSE infrastructure and prompt, cancel, and permission actions can remain ordinary POST requests. The event payloads are middleman types, not raw ACP messages. If interactive bidirectional needs grow, the same normalized event model can move behind a WebSocket later.

API routes should be registered through Huma with shared request/response types, and generated API artifacts must be refreshed with `make api-generate` in the implementation slice that introduces these routes. Integration-style API tests should use the generated Go client where practical.

File suggestion APIs use the same allowed-root policy as ACP filesystem capabilities. Workspace file search derives its root from `workspace_id`; ambient file search requires `cwd` under a configured ambient allowed root. Results contain relative path, display label, URI, and MIME type hint. The API should ignore common heavy directories such as `.git`, dependency caches, build outputs, and hidden runtime folders unless a later configuration explicitly includes them.

Skill suggestion APIs do not execute skills. They return metadata only: name, description, source, and optional tags. The first slice uses configured local skill manifests or agent-profile skill lists; it does not crawl arbitrary directories from browser input.

ACP slash commands are separate from middleman skills. The agent may advertise slash commands dynamically with `available_commands_update`, and the composer should merge those commands into `/` suggestions with a different source marker. Selecting an ACP slash command inserts the command text into the prompt; selecting a middleman skill inserts structured skill metadata that middleman resolves server-side.

## Data Model

Persist session metadata and transcript events so the UI can survive refreshes and workspace navigation.

Add tables conceptually shaped as:

- `acp_agents`: optional persisted agent metadata discovered from `initialize`, keyed by configured agent key.
- `acp_agent_capabilities`: latest capability snapshot from `initialize`, keyed by configured agent key and protocol version.
- `acp_sessions`: middleman session ID, ACP agent session ID, agent key, nullable workspace ID, cwd, additional directories, status, title, agent metadata, config-option state, created/updated timestamps, and last error.
- `acp_events`: session ID, monotonic sequence, event kind, role, JSON payload, created timestamp.
- `acp_permission_requests`: session ID, request ID, tool call payload, option payloads, status, selected option, created/resolved timestamps.
- `acp_session_commands`: latest ACP-advertised slash commands for a session.
- `acp_tool_calls`: latest normalized tool-call state per session and `toolCallId`, including redacted content and locations for replay without reprocessing every patch event.

The transcript event table stores normalized UI events rather than protocol messages. Raw ACP payloads can be included in a nested diagnostic field for unknown event kinds, but the primary renderer should rely on stable middleman event kinds.

All timestamps follow the project UTC policy.

Database migrations should add indexes for workspace/session lookup and event replay by session sequence. Existing databases without ACP tables continue to start normally through the existing migration path.

Transcript retention is bounded. The first slice keeps a configurable maximum number of events per session and a maximum byte size per JSON payload. Captured stderr, command output, and raw diagnostic ACP payloads are truncated before persistence.

Sensitive values are redacted before persistence or UI display. Redaction covers configured token environment variable names, common credential key patterns, agent stderr, command errors, tool output summaries, and any raw diagnostic payload stored for unknown ACP updates.

## Event Contract

The backend normalizes ACP messages into append-only session events. Each event has:

- `id`: database ID.
- `session_id`: middleman ACP session ID.
- `seq`: monotonically increasing integer within the session.
- `kind`: stable UI event kind.
- `role`: `user`, `assistant`, `tool`, `system`, or `permission`.
- `payload`: event-specific JSON.
- `created_at`: UTC timestamp.

Initial `GET /api/v1/acp/sessions/{id}` returns session metadata and the full retained transcript ordered by `seq`. The SSE endpoint starts by replaying retained events after an optional `after_seq` query parameter, then streams live events. Duplicate delivery is allowed across reconnects; the frontend de-duplicates by `seq`.

First-slice event kinds:

- `session_status`: payload has `status`, optional `reason`, and optional `error`.
- `session_info`: payload has optional `title`, optional `updated_at`, and redacted `_meta` from `session_info_update`.
- `session_config_options`: payload has the complete current config-option list after session creation returns `configOptions`, `session/set_config_option`, or `config_option_update`.
- `available_commands`: payload has the complete current slash-command list from `available_commands_update`.
- `user_message`: payload has `content` blocks.
- `assistant_message_chunk`: payload has `message_id`, `content`, and `append`.
- `process_plan_snapshot`: payload has `plan_id`, `entries`, and `source: "acp_plan"`. Each entry has stable `entry_id`, `content`, normalized `status`, optional `priority`, and original ACP fields.
- `process_plan_delta`: payload has `plan_id`, changed `entries`, and previous/current aggregate counts. The first slice may synthesize deltas by comparing consecutive ACP plan snapshots.
- `tool_call`: payload has `tool_call_id`, merged patch state, `title`, `status`, optional `kind`, redacted `summary`, optional `locations`, and optional redacted content summary.
- `tool_call_content_chunk`: payload has `tool_call_id` and one redacted content item. This is a compatibility event for ACP v2 draft agents; v1 agents still use replacement `content` arrays on `tool_call_update`.
- `permission_request`: payload has `request_id`, `tool_call`, `options`, and `status`.
- `permission_resolution`: payload has `request_id`, `outcome`, and optional `option_id`.
- `error`: payload has `message`, optional `code`, and optional redacted diagnostics.
- `unknown`: payload has `acp_type` and a redacted diagnostic summary.

Status transitions are explicit: `starting`, `idle`, `running`, `waiting_for_permission`, `cancelling`, `cancelled`, `closing`, `closed`, and `errored`. The manager allows one active prompt per session. A second prompt request while a turn is active returns a conflict response rather than queueing. Duplicate cancel requests are idempotent while the session is `running`, `waiting_for_permission`, or `cancelling`. Duplicate close requests are idempotent once the session is `closing` or `closed`.

Only one permission request may be active for a session in the first slice. If an agent sends another permission request before the first resolves, the manager returns an error to the agent and appends an `error` event.

## Message Rendering

User and assistant text content blocks are rendered as GitHub Flavored Markdown (GFM). The renderer must support:

- tables.
- fenced code blocks.
- inline code.
- task list checkboxes.
- blockquotes.
- links.
- ordered and unordered lists.

Markdown rendering is presentation-only. The persisted event payload keeps the original text content blocks, and the frontend turns them into sanitized HTML or native Svelte output at render time. Raw HTML in model output is disabled or sanitized so agent messages cannot inject scripts, event handlers, unsafe URLs, or arbitrary application markup.

GFM tables should be horizontally scrollable inside the message bubble or message body rather than forcing the whole chat surface wider. Long code blocks and table cells use existing app overflow patterns so the transcript remains usable on narrow workspace panes.

Structured ACP events do not become Markdown. Process plans, tool calls, permission prompts, errors, and autocomplete chips render through dedicated components so their status, controls, and accessibility semantics remain reliable.

## Process Indicators

Process indicators are derived from ACP plan/task-list updates and tool-call status, but the plan is the primary source. The manager stores the latest plan state per session in addition to the append-only event log so list and tab surfaces can show progress without replaying the entire transcript.

Plan entry statuses are normalized to `pending`, `in_progress`, `completed`, `failed`, `cancelled`, or `unknown`. Unknown ACP statuses are preserved in the entry payload and displayed as `unknown` until the UI learns a better mapping.

The aggregate process state includes:

- total task count.
- counts by normalized status.
- the first `in_progress` task content, if present.
- the latest changed task content.
- whether the plan has blocked, failed, or cancelled entries.
- the update timestamp.

`AgentPlanView.svelte` renders the full task list inside the chat surface. Smaller surfaces, such as workspace tabs or a future floating sidebar header, consume the aggregate state to show compact process indicators like progress count, active task text, and failure/blocking state.

Plan entries are keyed by stable IDs where the agent supplies them. When ACP entries have no ID, middleman derives a stable entry ID from the normalized entry content and first-seen position within the current plan. Reordered entries keep their derived ID when content still matches.

Tool calls are secondary process evidence. They can update activity text and running state, but they do not replace the current plan unless no plan has been emitted for the session.

## Tool Call Grouping

ACP v1 emits `tool_call` creation notifications and `tool_call_update` patch notifications per `toolCallId`. Some agents already send only `tool_call_update`, and the ACP v2 draft proposes making that the single upsert notification. Middleman therefore treats both v1 event names as the same normalized upsert path: if a `toolCallId` has not been seen, create a default row; otherwise patch the existing row.

Patch semantics matter. Omitted fields leave existing values unchanged, concrete fields replace previous values, and a `null` field clears the value when the protocol version supports explicit clears. For collection fields such as content and locations, replacement updates replace the whole array. If a v2-capable agent sends `tool_call_content_chunk`, middleman appends the single chunk item in receive order unless a later replacement update clears or replaces the content. Middleman persists normalized tool-call events individually and stores the latest merged state so status updates, locations, and redacted content summaries remain inspectable.

The frontend groups successive tool calls as a presentation rule. A group is a contiguous run of tool-call display items within one prompt turn, uninterrupted by user messages, assistant message chunks, process plan snapshots, permission prompts, or errors. Updates for an existing `toolCallId` update that tool's row inside its original group instead of creating a new display row.

Collapsed groups show:

- group count and aggregate status.
- the first two tool calls in sequence.
- the last two tool calls in sequence.
- a hidden-count affordance when more than four tool calls exist.

Groups with four or fewer tool calls show every row by default. Groups with five or more show the first two and last two by default. Users can expand the group to show all tool calls and collapse it again. Expanded/collapsed state is browser-local UI state and is not persisted in the database.

Aggregate group status is `running` if any visible or hidden tool call is `pending` or `in_progress`, `failed` if any tool call failed and none are running, `completed` if all tool calls completed, and `mixed` for any other combination. The group header should expose enough summary text for compact surfaces without hiding failures.

## Frontend Architecture

Create a detachable chat surface under `frontend/src/lib/components/agent/` and API/store modules under `frontend/src/lib/api/acp.ts` and `frontend/src/lib/stores/agent-chat.svelte.ts`.

The core component is `AgentChatSurface.svelte`. It accepts a context object rather than reading workspace route state directly:

```ts
type AgentChatContext =
  | {
      scope: "workspace";
      workspaceId: string;
      worktreePath: string;
      repoLabel: string;
      itemLabel: string;
    }
  | {
      scope: "ambient";
      cwd?: string;
      additionalDirectories?: string[];
    };
```

Workspace mode passes this context from `WorkspaceTerminalView.svelte`. The workspace `worktreePath` prop is display context only; session creation still sends `workspace_id` as the authority and lets the server derive ACP `cwd`. A future ambient sidebar can pass `scope: "ambient"` without creating a fake workspace.

The component tree should be:

- `AgentChatSurface.svelte`: owns layout, session selection, connection lifecycle, and high-level actions.
- `AgentThread.svelte`: renders the ordered transcript.
- `AgentMessage.svelte`: renders user and assistant text content as sanitized GFM Markdown with table support.
- `AgentPlanView.svelte`: renders the current process plan/task list with per-entry status and compact aggregate progress.
- `AgentToolCallGroup.svelte`: groups successive tool calls, showing the first two and last two by default with an expand control.
- `AgentToolCall.svelte`: renders one tool call summary and result inside a group.
- `AgentPermissionPrompt.svelte`: renders ACP permission options and posts the selected outcome.
- `AgentSessionConfigControls.svelte`: renders ACP session configuration options in agent-provided order and posts option changes.
- `AgentComposer.svelte`: sends prompts and exposes cancel while a turn is running.
- `AgentMentionMenu.svelte`: renders `@` file suggestions and `/` skill suggestions for the composer.

The component must avoid workspace-specific UI assumptions. It can render compact context labels supplied by props, but it should not import workspace list state, PR detail state, terminal tab state, or router helpers.

`AgentComposer.svelte` supports two mention modes:

- `@`: opens file autocomplete after the user types `@` and filters by the current token. Selecting a file inserts a compact mention chip and stores the file URI plus path metadata separately from the visible text. On submit, selected files are sent as ACP resource content blocks or resource references according to the selected agent's prompt capabilities. The visible text still contains the mention label so the transcript remains readable.
- `/`: opens command autocomplete when typed at the start of the composer or after whitespace. Suggestions include ACP-advertised slash commands and middleman-configured skills. Selecting an ACP command inserts command text that the agent receives as a normal prompt string. Selecting a middleman skill inserts a skill chip and adds a structured skill mention to the outgoing prompt metadata. The backend resolves the selected skill against the configured catalog and includes the skill name and description in the prompt context; loading full skill instructions is a later extension unless the configured skill provider explicitly marks the skill as safe to embed.

Autocomplete menus are keyboard-first: arrow keys move selection, Enter accepts, Escape closes, and Tab accepts when a menu is open. Suggestions are debounced, cancel stale requests, and preserve typed text if the menu closes without selection.

## Workspace UX

Workspaces mode gains an Agent panel or tab that lives beside the existing Home, tmux, shell, and runtime session surfaces. The current launch cards remain for terminal-based sessions. ACP agents appear in a separate chat entry point so users understand they are opening a structured conversation rather than a raw terminal.

When opening agent chat from a workspace, the first prompt composer starts with the workspace context already attached server-side. The UI should show the repo, item number, branch, and worktree path in compact metadata near the thread header.

The right PR/issue/reviews sidebar remains independent. Agent chat should not require the PR sidebar to be open and should continue working if the workspace belongs to an issue rather than a pull request.

## Ambient UX

Ambient chat is not implemented in the first slice, but the design keeps it reachable.

Ambient sessions use the same `AgentChatSurface`, same ACP routes, and same backend manager. The differences are:

- `workspace_id` is null.
- `cwd` is optional.
- filesystem capabilities are limited to configured or explicitly selected roots.
- the launcher is a floating sidebar entry point rather than a workspace tab.

The floating sidebar can later be mounted near the app shell, using the existing embedded-layout and sidebar patterns where possible. It should not require a selected repository or configured workspace.

## Configuration

Add ACP agent configuration to the existing settings/config path rather than hard-coding agent commands.

Each agent profile includes:

- key
- label
- command and args
- optional env allowlist or explicit env additions
- whether filesystem write capability is advertised
- whether terminal capability is advertised
- optional skill catalog entries or a reference to a local skill manifest source
- optional ambient allowed roots

Credentials should follow the existing runtime behavior: strip server credentials by default and only pass explicitly configured environment values to launched agents.

## Resource Management

The manager enforces lifecycle bounds:

- idle agent processes are stopped after a configurable timeout when no active prompt or subscriber remains.
- SSE subscribers are removed promptly on disconnect and do not keep sessions alive forever.
- stderr capture is bounded per process.
- command execution for ACP terminal callbacks has a timeout and output limit.
- workspace teardown cancels active workspace-bound prompt turns, calls `session/close` for supported agents, and closes the agent process only after the ACP close path has completed or timed out.

## Error Handling

Initialization failures produce a disabled agent entry with the command error and captured stderr summary. Session creation failures return a structured API error and show an inline chat error. Prompt failures append an error event to the transcript and move the session back to idle unless the transport died. Transport death marks active sessions as errored and closes their streams.

Cancellation is best-effort. The UI immediately shows a cancelling state, sends `session/cancel`, and then waits for the original prompt result. If the agent exits or returns an error during cancellation, the session records a cancelled or errored stop reason based on the observable outcome.

Close is separate from cancel. Closing a session should call `session/close` when the agent advertises `sessionCapabilities.close`; otherwise middleman marks the local session closed and tears down its transport when no other sessions need it. A successful close records `closed`, keeps the retained transcript for reload, and prevents new prompt turns for that middleman session.

Permission requests time out only if the browser disconnects and the session is explicitly cancelled. A refresh should reload the pending permission prompt from persisted state.

## Implementation Slices

Implementation should land in reviewable slices:

1. Add ACP agent configuration types and fake-agent test fixtures without user-visible UI.
2. Add `internal/acp` transport with initialize handshake, stderr capture bounds, and fake-agent tests.
3. Add database migrations, query helpers, capability snapshots, session metadata, initial and updated command/config state, latest tool-call state, and session manager state transitions.
4. Add Huma API routes for agents, sessions, prompt, cancel, close, permissions, config options, commands, and SSE; regenerate API artifacts with `make api-generate`.
5. Add prompt streaming through the fake ACP agent and full-stack API plus SQLite e2e coverage.
6. Add ACP plan capture as durable process indicators with snapshot events, aggregate state, and tests for status normalization.
7. Add ACP session info, config-option, and available-command event handling with persistence and API exposure.
8. Add tool-call upsert handling that accepts v1 `tool_call`, v1 `tool_call_update`, permission-request tool-call payloads, and v2 draft `tool_call_content_chunk`.
9. Add filesystem and terminal client capability handlers with allowed-root, timeout, truncation, and redaction tests.
10. Add file, skill, and slash-command suggestion APIs for `@` and `/` composer autocomplete with allowed-root and configured-catalog tests.
11. Add permission request persistence and browser refresh recovery.
12. Add cancel and close paths, including `session/close` capability detection and local fallback behavior.
13. Add the detachable Svelte chat components, sanitized GFM message rendering, process indicator rendering, grouped tool-call rendering, config controls, composer autocomplete, and store with mocked API tests.
14. Mount the chat surface in workspaces mode without changing existing terminal session behavior.
15. Add final full-stack e2e coverage for workspace chat creation, prompt streaming, process plan indicators, command/config updates, file mention submission, slash-command submission, skill mention submission, cancellation, close, permission resolution, transcript reload, and root rejection.

## Testing

Backend tests should include a fake ACP agent process that speaks newline-delimited JSON-RPC. Tests should cover:

- initialize handshake and capability capture.
- session creation with workspace cwd.
- prompt streaming into normalized transcript events.
- tool call create/update notifications remain persisted as individual events with status updates tied to `toolCallId`.
- tool call updates can create rows without a prior create notification, merge patches without clearing omitted fields, clear fields when `null` is supported, and append v2 draft content chunks in order.
- ACP plan updates persisted as process indicator snapshots with aggregate progress counts.
- session info, config-option, and available-command updates persisted and replayed.
- session close calls `session/close` when supported and falls back to local closure when unsupported.
- cancellation forwarding.
- permission request pause and resolution.
- filesystem requests rejected outside the allowed root.
- file autocomplete never returns paths outside the workspace or ambient allowed root.
- skill autocomplete returns only configured skill metadata and does not execute or crawl untrusted paths.
- ambient sessions created without a workspace ID.
- SSE event replay and live streaming with real SQLite.

Frontend tests should cover:

- `AgentChatSurface` renders workspace and ambient contexts from props.
- transcript rendering for user text, assistant chunks, GFM tables, fenced code, task lists, process plans, tool calls, permission prompts, and errors.
- Markdown sanitization blocks raw script execution, unsafe links, event attributes, and arbitrary app markup.
- successive tool calls collapse into groups that show the first two and last two by default, expand to all rows, and update rows in place as `toolCallId` updates arrive.
- compact process indicators update when plan task statuses change.
- composer disables send and exposes cancel during an active turn.
- `@` autocomplete searches files, inserts mention chips, and includes selected resources on submit.
- `/` autocomplete searches ACP slash commands and middleman skills, inserts command text or skill chips, and includes selected skill metadata on submit.
- permission option clicks call the API and update local pending state.
- session config controls render options in agent-provided order and refresh when option updates arrive.
- workspace mode mounts the chat surface without coupling it to terminal tabs.

End-to-end tests should exercise the full stack with a fake ACP agent, real SQLite, and the generated Go API client where practical. Go tests run with `-shuffle=on`; frontend commands use Bun.

## Non-Goals

- Do not implement a general external worktree browser.
- Do not expose raw ACP JSON-RPC directly to browser code.
- Do not replace existing terminal runtime sessions.
- Do not add MCP server management in the first slice.
- Do not implement multiplayer shared agent sessions.
- Do not grant ambient filesystem write access without an explicit allowed root.
- Do not build the floating sidebar UI in the first workspace-focused implementation slice.
