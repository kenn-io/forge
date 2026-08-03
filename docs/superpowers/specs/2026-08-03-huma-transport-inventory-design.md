# Huma Transport Inventory Design

## Goal

Generate a deterministic, machine-readable inventory of Kenn Forge's
long-lived HTTP and WebSocket routes from the Huma operations that register
them. Embedding hosts and transport proxies can use the inventory in contract
tests without maintaining a second route list.

## Problem

The public OpenAPI document describes ordinary REST operations and
first-party server-sent event responses, but it intentionally excludes hidden
terminal WebSocket operations. Generic proxy operations also hide the finite
downstream paths and query modes that return long-lived streams. A consumer
that needs different handling for ordinary HTTP, streaming HTTP, and
WebSockets must currently rediscover those cases from implementation details.

A hand-written Markdown or JSON inventory would drift as routes change. The
inventory must instead observe the same Huma operations used by the running
server, with explicit registration metadata only where a proxy boundary makes
automatic inference impossible.

## Selected design

### Record registered operations

Forge will add a recording adapter around its Huma adapter. The recorder
observes each `huma.Operation` handed to the adapter, including hidden
operations registered directly through `Adapter().Handle`. Inventory
generation will assemble the same REST, terminal-WebSocket, and proxy Huma
route sets used by the server.

The recorder will retain only transport-contract fields. It will not retain
handlers, schemas, credentials, runtime state, or request data.

### Derive transport entries

The inventory generator will classify operations as follows:

- a response content entry of `text/event-stream` produces an `http-stream`
  route with that request media type;
- operations registered on the terminal Huma API produce `websocket` routes;
- a proxy operation may declare finite long-lived downstream variants in Huma
  operation metadata when its catch-all path prevents response inference.

Proxy annotations live at the Huma registration boundary. There is no
separate production inventory file.

The schema is versioned:

```json
{
  "schema_version": 1,
  "routes": [
    {
      "method": "GET",
      "path": "/api/v1/events",
      "transport": "http-stream",
      "accept": "text/event-stream"
    },
    {
      "method": "GET",
      "path": "/ws/v1/workspaces/{id}/terminal",
      "transport": "websocket"
    }
  ]
}
```

Routes are sorted by method, path, transport, and media type before encoding.
The optional `query` object identifies values that select a streaming mode on
an otherwise ordinary endpoint.
Duplicate entries, malformed absolute paths, unknown metadata shapes, and
unsupported transport values are generation errors.

### Developer and CI command

A developer command parallel to `cmd/kenn-forge-openapi` will write the JSON
inventory to standard output. It will construct route registrations without
starting listeners, opening the database, or launching background workers.
The command is tooling, not a public runtime subcommand or HTTP endpoint.

## Verification

Focused Forge tests will prove that:

- visible SSE operations are discovered from Huma response metadata;
- hidden local, runtime-session, and Fleet terminal operations are discovered;
- annotated proxy stream variants are included;
- output ordering and JSON encoding are deterministic;
- malformed or duplicate metadata fails generation;
- the generated inventory contains every long-lived route mode that Forge's
  request tracing excludes from ordinary bounded spans.

## Non-goals

- Publishing Forge's internal HTTP API as a supported public contract.
- Exposing live connection counts or request details at runtime.
- Defining how a particular embedding host implements its transport.
- Maintaining a Markdown or hand-written JSON route list.
- Changing terminal, stream replay, proxy, or authorization semantics.
