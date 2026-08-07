# MCP Companion

- MCP is a curated companion over the authenticated daemon, never a direct DB
  client or OpenAPI mirror (`internal/mcpserver/daemon.go::daemonClient`).
- Use only canonical `kenn-forge` command/resource/prompt names and
  `kenn_forge_*` tools; do not add legacy aliases (`internal/mcpserver/server.go::Server.registerTools`).
- The surface may write local workflow/workspace state but must not expose
  provider mutations, arbitrary commands, terminal bytes, or lifecycle cleanup.
- HTTP transport is loopback-only with independent bearer, Host, and Origin
  enforcement (`internal/mcpserver/http.go::Server.httpGuard`).
- Treat only typed not-stacked responses as absence; surface other evidence
  failures and structured retry/ambiguity state (`internal/mcpserver/tools_stack.go::isStackAbsentError`).
- Confirm a mutation only from exactly one complete JSON response value;
  malformed or trailing response data is ambiguous and non-retryable
  (`internal/mcpserver/daemon.go::daemonClient.do`).
- Initial-message receipts store no prompt or digest and survive runtime-row
  cleanup; only workspace deletion cascades them (`internal/db/migrations/000047_agent_initial_message_receipts.up.sql`).
- Receipt reuse requires matching agent, coding session, and normalized byte
  count; recover pending rows only after the daemon runtime lock is held, never
  during generic DB open (`internal/db/queries_agent_message.go::DB.ReserveAgentInitialMessage`, `cmd/kenn-forge/main.go::run`).
- Initial input requires exact live hook identity, LF or printable Unicode, and
  tracked bracketed paste for multiline text. Proven no-write rejection releases
  its reservation; possible writes finalize without client cancellation (`internal/server/workspaceapi/initial_message.go::Handler.submitInitialMessage`).
- MCP-created PR/issue workspaces must set `suppress_auto_assign`; ordinary UI
  omission preserves configured self-assignment (`internal/server/workspaceapi/routes_handlers.go::Handler.createWorkspace`).
- MCP can create/reuse PR, issue, or ad-hoc workspaces and launch one new agent
  runtime with one initial message. Ambiguous mutations are never retried or
  cleaned up; only a lost message response permits receipt-only recovery (`internal/mcpserver/tools_agent_spawn.go::Server.spawnWorkspaceWithAgent`).
