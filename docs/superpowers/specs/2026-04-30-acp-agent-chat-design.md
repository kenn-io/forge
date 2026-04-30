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

## ACP Protocol Model

Middleman acts as an ACP client. The server launches an ACP-compatible agent process on demand and speaks newline-delimited UTF-8 JSON-RPC over the process stdin/stdout transport. Agent stderr is captured for diagnostics and never parsed as ACP protocol data.

Each agent connection starts with `initialize`, where middleman advertises client capabilities such as filesystem access and terminal support. The agent response supplies protocol version, agent metadata, supported prompt capabilities, session loading support, MCP transport capabilities, and authentication methods.

Workspace chat creates an ACP session with `session/new` using:

- `cwd`: the workspace `worktree_path`.
- `additionalDirectories`: an empty list in the first slice.
- `mcpServers`: an empty list in the first slice.

Loaded or resumed conversations can later use `session/load`, `session/resume`, or `session/fork` if the selected agent supports them. User turns use `session/prompt` with text blocks and optional resource blocks. During a turn, the agent streams `session/update` notifications for user message replay, assistant message chunks, plans, thoughts, tool calls, and mode changes. Cancellation uses `session/cancel`.

Relevant ACP documentation:

- https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/transports.mdx
- https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/initialization.mdx
- https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/prompt-turn.mdx
- https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/tool-calls.mdx

## Backend Architecture

Add an `internal/acp` package with three focused responsibilities.

`transport` owns one launched agent process. It starts the configured command, sends JSON-RPC requests, maps response IDs back to callers, routes notifications, handles agent-to-client requests, records stderr diagnostics, and shuts down the process when no sessions need it.

`manager` owns middleman ACP sessions. It creates sessions, binds them to a transport, stores live status, appends normalized transcript events, broadcasts updates to subscribers, and exposes prompt and cancel operations to HTTP handlers. A session has a middleman session ID, optional workspace ID, selected agent key, ACP agent session ID, cwd, status, timestamps, and last error.

`clientcaps` implements the client-side ACP methods middleman chooses to advertise. Filesystem reads and writes are scoped to allowed roots. For workspace sessions, the only allowed root is the workspace worktree path. For ambient sessions, allowed roots must come from explicit server configuration or a future user-selected root; an ambient session with no allowed root advertises no write capability.

Terminal callbacks use short-lived, non-interactive command execution in the session cwd for the first slice. They do not attach to the existing browser terminal pane. Long-running interactive terminals remain the job of the existing local runtime. This keeps ACP terminal callbacks predictable and avoids mixing structured chat events with raw PTY streams.

Permission requests from the agent are normalized into pending permission events and sent to the UI. The manager pauses the JSON-RPC response until the user selects one of the ACP-provided options or cancels the turn.

## Server API

Expose ACP through middleman-native REST and streaming APIs rather than exposing raw JSON-RPC directly to the browser.

New routes:

- `GET /api/v1/acp/agents`: list configured ACP agents, availability, labels, and disabled reasons.
- `POST /api/v1/acp/sessions`: create a session. Body includes `agent_key`, optional `workspace_id`, optional `cwd`, and optional initial prompt.
- `GET /api/v1/acp/sessions`: list recent sessions, filterable by workspace ID.
- `GET /api/v1/acp/sessions/{id}`: return session metadata and transcript.
- `POST /api/v1/acp/sessions/{id}/prompt`: send a user prompt.
- `POST /api/v1/acp/sessions/{id}/cancel`: cancel the active prompt turn.
- `POST /api/v1/acp/sessions/{id}/permissions/{request_id}`: resolve a pending ACP permission request.
- `GET /api/v1/acp/sessions/{id}/events`: stream normalized session events.

Use SSE for the first browser streaming path because the app already has SSE infrastructure and prompt, cancel, and permission actions can remain ordinary POST requests. The event payloads are middleman types, not raw ACP messages. If interactive bidirectional needs grow, the same normalized event model can move behind a WebSocket later.

## Data Model

Persist session metadata and transcript events so the UI can survive refreshes and workspace navigation.

Add tables conceptually shaped as:

- `acp_agents`: optional persisted agent metadata discovered from `initialize`, keyed by configured agent key.
- `acp_sessions`: middleman session ID, ACP agent session ID, agent key, nullable workspace ID, cwd, status, title, created/updated timestamps, and last error.
- `acp_events`: session ID, monotonic sequence, event kind, role, JSON payload, created timestamp.
- `acp_permission_requests`: session ID, request ID, tool call payload, option payloads, status, selected option, created/resolved timestamps.

The transcript event table stores normalized UI events rather than protocol messages. Raw ACP payloads can be included in a nested diagnostic field for unknown event kinds, but the primary renderer should rely on stable middleman event kinds.

All timestamps follow the project UTC policy.

## Frontend Architecture

Create a detachable chat surface under `frontend/src/lib/components/agent/` and API/store modules under `frontend/src/lib/api/acp.ts` and `frontend/src/lib/stores/agent-chat.svelte.ts`.

The core component is `AgentChatSurface.svelte`. It accepts a context object rather than reading workspace route state directly:

```ts
type AgentChatContext =
  | {
      scope: "workspace";
      workspaceId: string;
      cwd: string;
      repoLabel: string;
      itemLabel: string;
    }
  | {
      scope: "ambient";
      cwd?: string;
      additionalDirectories?: string[];
    };
```

Workspace mode passes this context from `WorkspaceTerminalView.svelte`. A future ambient sidebar can pass `scope: "ambient"` without creating a fake workspace.

The component tree should be:

- `AgentChatSurface.svelte`: owns layout, session selection, connection lifecycle, and high-level actions.
- `AgentThread.svelte`: renders the ordered transcript.
- `AgentMessage.svelte`: renders user and assistant message content.
- `AgentPlanView.svelte`: renders streamed plan entries with status.
- `AgentToolCall.svelte`: renders tool call summaries and results.
- `AgentPermissionPrompt.svelte`: renders ACP permission options and posts the selected outcome.
- `AgentComposer.svelte`: sends prompts and exposes cancel while a turn is running.

The component must avoid workspace-specific UI assumptions. It can render compact context labels supplied by props, but it should not import workspace list state, PR detail state, terminal tab state, or router helpers.

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
- optional ambient allowed roots

Credentials should follow the existing runtime behavior: strip server credentials by default and only pass explicitly configured environment values to launched agents.

## Error Handling

Initialization failures produce a disabled agent entry with the command error and captured stderr summary. Session creation failures return a structured API error and show an inline chat error. Prompt failures append an error event to the transcript and move the session back to idle unless the transport died. Transport death marks active sessions as errored and closes their streams.

Cancellation is best-effort. The UI immediately shows a cancelling state, sends `session/cancel`, and then waits for the original prompt result. If the agent exits or returns an error during cancellation, the session records a cancelled or errored stop reason based on the observable outcome.

Permission requests time out only if the browser disconnects and the session is explicitly cancelled. A refresh should reload the pending permission prompt from persisted state.

## Testing

Backend tests should include a fake ACP agent process that speaks newline-delimited JSON-RPC. Tests should cover:

- initialize handshake and capability capture.
- session creation with workspace cwd.
- prompt streaming into normalized transcript events.
- cancellation forwarding.
- permission request pause and resolution.
- filesystem requests rejected outside the allowed root.
- ambient sessions created without a workspace ID.
- SSE event replay and live streaming with real SQLite.

Frontend tests should cover:

- `AgentChatSurface` renders workspace and ambient contexts from props.
- transcript rendering for user text, assistant chunks, plans, tool calls, permission prompts, and errors.
- composer disables send and exposes cancel during an active turn.
- permission option clicks call the API and update local pending state.
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
