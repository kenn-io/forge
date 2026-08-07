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
