# Version API Build Metadata Design

## Goal

Expand `GET /api/v1/version` so it returns the same build metadata as
`middleman version --json` while preserving the existing route and `version`
field.

## API Contract

The successful response remains an HTTP 200 JSON object and contains four
required string fields:

```json
{
  "name": "middleman",
  "version": "1.2.3",
  "commit": "abc1234",
  "buildDate": "2026-07-12T12:00:00Z"
}
```

The values come from the same linker-provided `version`, `commit`, and
`buildDate` variables used by the CLI command. `name` is the constant
`middleman`. The route's authentication and error behavior do not change.

## Server Wiring

Replace the server's single version string with a build-information value.
Startup supplies all four fields after constructing the server, and the
existing version handler returns that value. This is a direct migration of
the internal setter; no compatibility wrapper is needed because only the
main package currently calls it.

The API returns the raw version and commit as separate fields, matching the
CLI JSON output. It does not synthesize the current `dev-<commit>` display
string.

## Verification

Add one focused server test that configures distinct build metadata, calls
the route through `ServeHTTP`, and asserts the response contract. Write and
run this test before implementation to demonstrate the missing fields. Do
not add a separate end-to-end or browser test for this static response.

After implementation, regenerate the checked-in OpenAPI document and Go API
client, review their diffs for the three added required fields, and run the
focused server tests. The existing CLI JSON test continues to protect the
command's matching response shape.
