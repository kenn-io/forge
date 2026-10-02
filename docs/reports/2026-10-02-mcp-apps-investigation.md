# Agent-generated components through MCP Apps

Research date: 2026-10-02. This report distinguishes inspected implementation
from browser verification and remaining interoperability work.

## Finding

MCP Apps standardizes approaches developed by MCP-UI and OpenAI's Apps SDK.
SEP-1865 is marked **Final**; MCP-UI also remains a separate SDK project. The
standard associates a tool with a predeclared HTML resource and lets that app
call tools through its host. This supports portable interactive components,
although each host still needs access to Forge's MCP endpoint. [1][2][3]

The prototype implements a generic rendering path. The agent supplies the HTML,
CSS, JavaScript, layout, and interactions. Forge supplies cached data operations
and a bridge. A PR dashboard is one possible agent-authored component; there is
no built-in PR dashboard template.

## What the prototype implements

`kenn_forge_render_app` accepts `{title, html}`. Its metadata points to
`ui://kenn-forge/generated-app`, a static HTML resource with the standard
`text/html;profile=mcp-app` MIME type. Its structured result carries the authored
component. An MCP Apps host can discover the renderer through the tool metadata
and deliver that result through the standard initialization and tool-result
messages. The renderer executes inline scripts after the bridge initializes.
This is a standard MCP Apps outer contract with a Forge-specific HTML authoring
contract. [2][9]

The same tool returns an embedded JSON resource identified by
`kenn-forge://apps/generated`. Forge's ACP chat recognizes that descriptor and
renders the component through the official `AppBridge` and
`PostMessageTransport` SDK classes. The descriptor carries the code in the
transcript; there is no component registry, generated ID, separate app server,
or additional persistence layer. The marker selects a renderer. It does not
establish provenance or grant privileges. All component code remains untrusted.
[9][10]

Generated scripts receive two operations:

- `window.app.callServerTool({name, arguments})` reads cached Forge data.
- `window.app.openLink({url})` requests an HTTP(S) link through the host.

Forge's backend independently restricts widget calls to eight existing cached
read tools: repositories, PR contexts, item context, activity, search, review
candidates, stack context, and workflow-state lists. It dispatches through the
existing MCP validation and result handling. The publishing tool and mutation
tools are outside that allowlist. External Apps hosts also apply their own tool
visibility and permission policy. [9][10]

"Live" means the authored component can call those reads again, for example
when the user clicks Refresh. It does not mean a provider sync is forced or
that the component subscribes to cache changes. The agent controls the display
and refresh interaction. Cached results already expose evidence freshness,
checks, and mergeability; a green badge must not certify permission to merge.
Bulk PR reads preserve pagination and provider-verified repository identity.
[9][11]

## Verification boundary

The full-stack Chromium test published agent-authored HTML through the real
Forge MCP server, delivered it through ACP tool content, and rendered it in
chat. A refresh changed the displayed CI status from pending to success after
the isolated test updated the SQLite-backed cache. The component rendered again
after a page reload. A browser probe using the official AppBridge also observed
host-DOM access denied and a CSP `connect-src` violation for a reachable URL.
No real external chat client has been tested in this investigation.

An external-host test must prove resource loading, execution, a cached read,
refresh after a cache change, and links with that host's connection policy.
Forge's loopback endpoint serves local clients. A remotely hosted chat cannot
reach the user's loopback address directly. Forge's existing Tailscale Serve
route has a separate access policy; its existence does not prove a hosted chat
connector can use it. [11]

## ACP and sandbox limits

ACP tool updates carry content and arbitrary raw input/output. An optional tool
name is informational, not authority. Those fields do not by themselves
establish an arbitrary MCP server's identity or a route for later widget calls.
The prototype therefore supports Forge's generated-component descriptor and
Forge-owned reads. It is not a general third-party MCP Apps host. The official
MCP-over-ACP proposal describes richer routing but remains a draft. [4][5]

For browser hosts, the Apps specification requires a sandbox proxy with a
different origin, `allow-scripts` and `allow-same-origin`, and CSP headers based
on resource policy. The official basic-host example uses a second HTTP origin
and HTTP CSP headers. [2][6]

Forge's prototype uses a `data:` proxy and an inner sandboxed document. Data
URLs have unique opaque origins, but the implementation uses CSP meta elements
rather than the specified HTTP headers. That is a known conformance gap, even
if browser tests demonstrate the intended restrictions. Do not describe this
as a complete, conforming general MCP Apps browser host. The portable resource
can still run under an external host's standard sandbox. [2][6][7][10]

The prototype intentionally allows inline model-authored code, with no external
imports, network, forms, or provider writes in its authoring contract. Actual
boundary claims require executable browser/API tests, not only inspecting CSP
or the allowlist. Code stored in a transcript also runs again when remounted;
restart/replay behavior depends on the existing ACP transcript lifecycle.

## Remaining effort

These are estimates for one engineer familiar with Forge, not measured delivery
times. The generic publishing tool and first-party ACP rendering already exist
in this branch; the ranges below cover work beyond inspected implementation.

| Work | Estimate | Main uncertainty |
| --- | --- | --- |
| Exercise common real ACP agents and finish lifecycle/permission coverage | 1–3 engineer-days | Agents preserving rich tool content, cancellation, and link gestures |
| Validate the same resource in one external chat host | 1–3 additional days | Host access and MCP transport-version interoperability |
| Bring Forge's sandbox deployment into full spec conformance | 3–7 additional days | HTTPS/remote separate-origin deployment and CSP headers |
| Host arbitrary third-party MCP Apps in ACP chat | 2–4 additional engineer-weeks | Server identity, resource discovery, routing, permissions, lifecycle |

The smallest useful demonstration is an agent-authored component that queries
Forge's cache, renders a custom layout, and refreshes after a cache update.
General third-party server management, saved dashboard configuration, provider
writes, and push subscriptions are separate work.

## Sources

1. [SEP-1865](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/seps/1865-mcp-apps-interactive-user-interfaces-for-mcp.md), status and rationale.
2. [MCP Apps specification](https://github.com/modelcontextprotocol/ext-apps/blob/main/specification/2026-01-26/apps.mdx), resource discovery, sandbox proxy, CSP, tool input/result, visibility, interactive updates.
3. [Official MCP Apps README](https://github.com/modelcontextprotocol/ext-apps/blob/main/README.md), SDK roles and upstream client-support claims.
4. [ACP v1 tool calls](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v1/tool-calls.mdx).
5. [MCP-over-ACP draft](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/mcp-over-acp.mdx).
6. [Official host example](https://github.com/modelcontextprotocol/ext-apps/blob/main/examples/basic-host/src/implementation.ts) and [sandbox proxy](https://github.com/modelcontextprotocol/ext-apps/blob/main/examples/basic-host/src/sandbox.ts).
7. [MDN data URLs](https://developer.mozilla.org/en-US/docs/Web/URI/Reference/Schemes/data), opaque-origin behavior.
9. Implementation: [rendering tool and read allowlist](../../internal/mcpserver/apps.go), [generic app resource](../../internal/mcpserver/app.html).
10. Implementation: [browser host](../../frontend/src/lib/components/acp/mcp-app.ts), [backend bridge](../../internal/server/mcp_apps.go).
11. Repository contracts: [MCP](../../context/mcp-server.md), [bulk PR contexts](../../internal/mcpserver/tools_pulls.go), [PR result fields](../../internal/mcpserver/tools_items.go).
