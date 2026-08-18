# MCP Companion

- MCP is disabled unless `[mcp].enabled = true`; an omitted or zero port uses
  the backend port plus one, while a nonzero port overrides it
  (`internal/config/config.go::Config.MCPPort`).
- MCP listeners are startup-bound, loopback-only, and use a distinct valid port
  (`internal/server/config_reload.go::startupConfigSnapshot`).
- Discovery publishes `mcp_listen_addr` through both daemon metadata surfaces
  when MCP is enabled (`internal/daemonruntime/runtime.go::NewIdentity`).
- MCP is a curated companion over the authenticated daemon, never a direct DB
  client or OpenAPI mirror (`internal/mcpserver/daemon.go::daemonClient`).
- Use only canonical `kenn-forge` command/resource/prompt names and
  `kenn_forge_*` tools; do not add legacy aliases (`internal/mcpserver/server.go::Server.registerTools`).
- The surface may write local workflow/workspace state but must not expose
  provider mutations, arbitrary commands, terminal bytes, or lifecycle cleanup.
- HTTP transport is loopback-only with independent bearer, Host, and Origin
  enforcement (`internal/mcpserver/http.go::Server.httpGuard`).
- Target MCP `2026-07-28`: keep HTTP sessionless and do not advertise deprecated
  logging or change notifications for the static catalog (`internal/mcpserver/server.go::New`, `internal/mcpserver/http.go::Server.RunHTTP`).
- Treat only typed not-stacked responses as absence; surface other evidence
  failures and structured retry/ambiguity state (`internal/mcpserver/tools_stack.go::isStackAbsentError`).
- Confirm a mutation only from exactly one complete JSON response value;
  malformed or trailing response data is ambiguous and non-retryable
  (`internal/mcpserver/daemon.go::daemonClient.do`).
- Initial-message attempts are process-local and retain the exact normalized
  prompt only in daemon memory. Same-daemon retries must match agent, coding
  session, and prompt; daemon restart intentionally permits a fresh attempt
  (`internal/server/workspaceapi/initial_message.go::initialMessageAttempt`).
- Initial input requires exact live hook identity, LF or printable Unicode, and
  tracked bracketed paste for multiline text. Proven no-write rejection releases
  its reservation; possible writes finalize without client cancellation (`internal/server/workspaceapi/initial_message.go::Handler.submitInitialMessage`).
- MCP-created PR/issue workspaces must set `suppress_auto_assign`; ordinary UI
  omission preserves configured self-assignment (`internal/server/workspaceapi/routes_handlers.go::Handler.createWorkspace`).
- MCP can create/reuse PR, issue, or ad-hoc workspaces and launch one new agent
  runtime with one initial message. Ambiguous mutations are never retried or
  cleaned up; lost message responses permit only bounded cancellation-independent
  status reads from the same daemon, and unresolved `pending` or `uncertain`
  evidence remains ambiguous (`internal/mcpserver/tools_agent_spawn.go::Server.recoverInitialMessageStatus`).
