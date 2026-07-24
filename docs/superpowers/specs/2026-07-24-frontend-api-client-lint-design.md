# Frontend API Client Lint Design

## Problem

Middleman already runs `frontend-api-client-check`, but the checker only looks
for the contiguous text `/api/v1`. A production helper assembled the same
prefix as `"/api" + "/v1"`, then built a browser image URL from it. The guard
passed even though the URL bypassed the configured API base and broke
deployments mounted below a non-root `base_path`.

The guard must enforce the API access policy rather than one spelling of the
forbidden string. Its diagnostic must also identify the approved replacement.

## Considered Approaches

1. Expand the text scanner with regular expressions for concatenated strings.
   This is a small patch, but every new expression shape creates another escape.
2. Parse frontend source and evaluate statically resolvable string expressions.
   This catches literals, concatenation, template strings, and constant aliases
   while retaining precise file and line diagnostics. This is the selected
   approach.
3. Replace the script with a custom general-purpose linter plugin. That offers
   deeper data-flow analysis, but adds integration and maintenance cost beyond
   the narrow policy being enforced.

## Design

The existing guardrail command and CI integration remain unchanged. The
checker will parse production JavaScript, TypeScript, JSX, TSX, and Svelte
script content and resolve constant string expressions far enough to recognize
the Middleman REST prefix even when it is split or passed through aliases.

Production REST requests must use the generated client. Provider-aware route
parameters continue to come from `providerItemPath`, `providerRepoPath`, and
the related typed helpers; those helpers return OpenAPI paths without an
`/api/v1` mount prefix.

Browser-owned resource navigation, such as an `<img src>`, cannot be performed
by `openapi-fetch`. It must use a shared resource-URL helper derived from the
same configured API base URL as the generated client. The Markdown image proxy
will use that helper, so non-root `base_path` deployments resolve correctly.

SSE, WebSocket, and NDJSON transports remain explicit exceptions because the
generated REST client does not implement their connection semantics. Each
exception stays narrowly associated with its existing transport rather than
becoming a general file or directory exemption.

## Diagnostics

Each violation will identify the approved replacement:

- REST request: use the generated runtime client or the injected typed UI
  client.
- Browser resource URL: use the configured API resource-URL helper.
- Streaming connection: use the named, allowlisted transport helper.

The message will not merely report that a URL is forbidden.

## Verification

Script tests will first reproduce the missed split-literal and constant-alias
case, then cover template construction, allowed generated-client base setup,
browser resource URLs, test/generated-file exclusions, and the existing
streaming exceptions. The production scan must flag the current Markdown image
URL before its fix and pass afterward.

Markdown rendering tests will cover both root and prefixed API bases. The normal
frontend guardrail, script-test, formatting, lint, type-check, and relevant UI
test lanes will run before completion. No OpenAPI operation changes are
expected; `make api-generate` must remain clean.
