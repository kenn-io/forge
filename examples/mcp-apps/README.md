# Agent-authored MCP Apps prototype

This directory is an example of content an agent can generate, not a dashboard
template built into Forge. The generic tool accepts any self-contained HTML
fragment with inline CSS and JavaScript.

Run this branch with the repository's isolated development command:

```sh
make dev-ephemeral
```

Use the printed frontend URL, open a workspace with an ACP agent that supports
HTTP MCP, and ask:

> Use Forge's MCP tools to build an interactive view of the PRs I should look at.
> Choose the layout yourself. Show CI, review and mergeability evidence, cache
> freshness, provider links and a refresh button. Use kenn_forge_render_app.
> Do not perform mutations.

To reproduce this example precisely, ask the agent to read
`examples/mcp-apps/pr-dashboard.html` and pass its contents as `html` to
`kenn_forge_render_app`, with a short `title`. That file uses
`window.app.callServerTool` and `window.app.openLink`, which the renderer supplies.
The agent must preserve the returned embedded resource in its ACP tool output.
Agents that flatten tool results to text will not render the component.

Refresh rereads the live Forge cache; it does not trigger provider sync.
Generated code is retained in the chat transcript and runs again when the
component mounts. There is no saved dashboard configuration or component store.

An external MCP Apps host discovers the same generic renderer through tool
metadata. External-client interoperability is not yet verified. Forge's own
browser host is a prototype with a known CSP-header conformance gap; see the
[investigation](../../docs/reports/2026-10-02-mcp-apps-investigation.md).
