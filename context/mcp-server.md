# MCP Companion

- MCP is an optional daemon-owned secondary listener enabled by
  `[mcp].enabled`; an omitted or zero port uses the backend port plus one, while
  a nonzero port overrides it (`internal/config/config.go::Config.MCPPort`).
- The listener is startup-bound and loopback-only. Discovery publishes
  `mcp_listen_addr` and `/api/ping` publishes `mcp_url`; changing listener
  settings requires a restart (`cmd/kenn-forge/main.go::bindDaemonListeners`,
  `internal/server/daemon_ping.go::Server.daemonPing`).
- MCP serves only `/mcp` over stateless Streamable HTTP. Authentication follows
  `[api].require_auth`; direct loopback peer, exact loopback authority, absent
  forwarding headers, and optional same-origin HTTP Origin are required
  (`internal/mcpserver/server.go::Server.HTTPHandler`,
  `internal/server/mcp_http.go::NewMCPHTTPGuard`).
- Tool implementations call the typed in-process Forge backend and never the
  daemon's public HTTP API (`internal/mcpserver/backend.go::Backend`,
  `internal/server/mcp_backend.go::Server.MCPBackend`).
- Target MCP `2026-07-28`; do not advertise deprecated logging or catalog
  change notifications for the static surface (`internal/mcpserver/server.go::New`).
- Use only canonical `kenn-forge` command/resource/prompt names and
  `kenn_forge_*` tools; do not add aliases (`internal/mcpserver/server.go::Server.registerTools`).
- The surface may write local workflow/workspace state but must not expose
  provider mutations, arbitrary commands, terminal bytes, lifecycle cleanup,
  or `removed_upstream` items through workflow reads or writes
  (`internal/server/mcp_backend.go::mcpBackend.ListWorkflowStates`,
  `internal/server/mcp_backend.go::mcpBackend.SetWorkflowState`).
- Candidate output defaults to 25 and caps at 100. Apply candidate `item_types`
  to Activity before its 5,000-row internal safety window
  (`internal/mcpserver/tools_candidates.go::Server.findReviewCandidates`,
  `internal/server/mcp_backend.go::mcpBackend.ListActivity`).
- Treat only typed not-stacked responses as absence; surface other evidence
  failures with structured retry and ambiguity state
  (`internal/mcpserver/tools_stack.go::isStackAbsentError`).
- Tool failures set MCP `isError` and preserve kind, code, retryability,
  ambiguity, and details as JSON content plus `io.kenn.forge/error` metadata;
  never reduce typed backend failures to message-only errors
  (`internal/mcpserver/tools_read.go::wrapTool`).
- Full-diff handoff stays within 10 MiB and represents binary, rename-only,
  copy-only, and other files without text hunks using minimal Git-style headers;
  one empty text patch must not discard the rest of the diff
  (`internal/mcpserver/tools_diff.go::serializeDiffPatches`).
- Initial-message attempts are process-local and retain the exact normalized
  prompt only in daemon memory. Same-daemon retries must match agent, coding
  session, and prompt; daemon restart permits a fresh attempt
  (`internal/server/workspaceapi/initial_message.go::initialMessageAttempt`).
- Initial input requires exact live hook identity, LF or printable Unicode, and
  tracked bracketed paste for multiline text. Proven no-write rejection releases
  its reservation; possible writes finalize without client cancellation
  (`internal/server/workspaceapi/initial_message.go::Handler.SubmitInitialMessageService`).
- MCP-created pull-request and issue workspaces suppress optional automatic
  assignment; ordinary UI omission preserves configured self-assignment
  (`internal/server/workspaceapi/routes_handlers.go::Handler.CreatePullWorkspace`,
  `internal/server/workspaceapi/routes_handlers.go::Handler.CreateIssueWorkspaceService`).
- MCP can create or reuse a pull-request, issue, or ad-hoc workspace and launch
  one new agent runtime with one initial message. Ambiguous mutations are never
  retried or cleaned up; only initial-message status receives a bounded,
  cancellation-independent read (`internal/mcpserver/tools_agent_spawn.go::Server.recoverInitialMessageStatus`).
- Handoff success and failure evidence uses `stage` plus
  `initial_message.state`; never add a separate `message_delivered` output or
  error detail (`internal/mcpserver/tools_agent_spawn.go::spawnWorkspaceWithAgentOutput`).
