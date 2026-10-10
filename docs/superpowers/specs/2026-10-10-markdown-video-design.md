# Markdown Video Playback Design

Temporary working artifact, committed at the maintainer's request while the
work is in progress. Distill into `context/` docs and delete before the PR.

## Goal

Play video inline in rendered markdown wherever the provider itself plays it:
PR and issue descriptions, comments, timeline review comments, and diff review
thread bubbles. No autoplay. Controls always on. The player never exceeds the
content width, including phone layouts. Private media loads through the
daemon with the repository credential, on default and host routes, streamed
with Range support and never buffered whole.

Out of scope: repository source-browser previews. GitHub's blob viewer shows
"View raw" for mp4 and webm files, so there is nothing to match.

## Verified provider behavior

- GitHub: a bare `https://<host>/user-attachments/assets/<id>` or
  `https://<host>/<owner>/<repo>/assets/<n>/<uuid>` URL alone in its own
  paragraph plays when the asset is video. The same URL after text on the next
  line, or pointing at an image, renders as a plain link. Raw `<video>` tags
  render only when `src` is an attachment URL; other sources are removed.
  Accepted uploads: mp4, mov, webm. The decision is made server-side from
  upload metadata; Forge only has the URL. The attachment URL 302-redirects to
  a GET-signed S3 URL that answers Range with 206 and the real video type and
  rejects HEAD.
- GitLab: image syntax whose extension is mp4, m4v, mov, webm, or ogv
  (case-insensitive) plays. Raw `<video>` tags are stripped. The uploads API
  answers `application/octet-stream` with attachment disposition, and serves
  Range through Workhorse.
- Gitea: raw `<video>` allowed; the editor inserts
  `<video src="attachments/<uuid>" controls>` (relative to the repository).
- Forgejo: raw `<video>` allowed; the copy-link button inserts
  `<video src="/attachments/<uuid>" controls>` (relative to the host).
- Bitbucket Cloud and Data Center: no video in markdown.

## Server

### Provider layer

- `platform.MarkdownMedia`: `Body io.ReadCloser`, `ContentType string`,
  `ContentLength int64` (-1 unknown), `ContentRange string`, `Partial bool`.
- `platform.MarkdownMediaReader`:
  `OpenMarkdownMedia(ctx, ref RepoRef, sourceURL, byteRange string) (MarkdownMedia, error)`.
  The caller closes `Body`. The stream lives until `Body` is closed or `ctx`
  ends.
- `Capabilities.ReadMarkdownMedia`, registry `MarkdownMediaReader(kind, host)`
  returning `UnsupportedCapability(kind, host, "read_markdown_media")`, API
  capability `read_markdown_media` in repository capabilities.
- New platform error codes, translated at the server boundary and added to
  the `context/error-handling.md` table:
  - `unsupported_media_type`: the source was fetched and is not an allowed
    video type. Wire result `415 unsupportedMediaType`. This is the only
    rejection that proves the asset is not a video.
  - `range_not_satisfiable`: upstream answered 416. Wire result
    `416 rangeNotSatisfiable`.
  A source URL outside the allowed shapes stays `invalid_argument`
  (`400 badRequest`).
- GitHub (`platform/github`, provider wrapper, `internal/github` routed
  client): accepts the two attachment URL shapes above on the platform host,
  the old shape only for the route's own repository. Uses the user credential
  like images. Forwards only `Range`. Accepts upstream `video/mp4`,
  `video/quicktime`, `video/webm`; any other upstream type is
  `unsupported_media_type`. The HTTP client has no whole-request timeout;
  only the wait for response headers is bounded.
- GitLab: accepts the same upload URLs as images; content type from the file
  extension: mp4/m4v `video/mp4`, mov `video/quicktime`, webm `video/webm`,
  ogv `video/ogg`; other extensions are `unsupported_media_type`. The
  foreground timeout bounds only the wait for response headers, not the body.
  The request keeps the existing auth transport's origin check, so a
  redirect to another origin fails instead of carrying the token.
- Gitea, Forgejo, Bitbucket: no capability.

### Route

- `GET /repo/{provider}/{owner}/{name}/markdown-media?source=` and
  `GET /host/{platform_host}/repo/{provider}/{owner}/{name}/markdown-media?source=`,
  operation IDs `get-markdown-media` and `get-markdown-media-on-host`, `Range`
  request header input. The OpenAPI responses declare 200 and 206 with the
  four allowed video types, like the image route declares its image types.
- Requires `read_markdown_media`. Opens the media before streaming so failures
  return normal problem responses.
- Success: status 200 or 206, `Content-Type`, `Content-Length` when known,
  `Content-Range` on 206, `Accept-Ranges: bytes`,
  `X-Content-Type-Options: nosniff`,
  `Cache-Control: private, max-age=31536000, immutable`; body copied as it
  arrives. No disk cache.
- The browser probe is this route with `Range: bytes=0-0`.

### Fleet

- `ProviderRouteRule.Streaming`, set for the two media routes (owner
  `ProviderHubOnly`, scope provider-read).
- `providerplane.Client` exposes only `Do`, and the hub client picks its
  no-timeout streaming transport only for the events scope. The client gains
  a way to send a provider-read request over that streaming transport
  (connect, TLS, and response-header bounds stay).
- For streaming rules `ProviderProxy` uses that path and copies the status,
  the safe headers (the existing filter already keeps `Content-Range` and
  `Accept-Ranges`), and the body as it arrives instead of the 32 MB buffer.
  Non-streaming routes are unchanged.

## Frontend

### Rules per provider (markdown with a repository)

| Provider | Player source | Raw `<video>` |
| --- | --- | --- |
| GitHub | Paragraph whose only content is a bare attachment URL, after the probe confirms video | Kept only when its source or a `<source>` child is an attachment URL, rewritten to the media route; otherwise removed |
| GitLab | Image syntax with a video extension; upload URLs rewritten to the media route | Removed |
| Gitea, Forgejo | none | Kept; `attachments/<uuid>` resolved against `https://<platformHost>/<repoPath>/` (the same base `item-reference.ts` uses for item links), `/attachments/<uuid>` against `https://<platformHost>/`; loaded directly |
| Bitbucket | none | Removed |

Markdown without a repository keeps raw `<video>` tags as today.

Gitea and Forgejo declare no markdown image or media capability, so their
attachments load directly from the browser, exactly like their images today.
Private Gitea and Forgejo attachments load only when the browser itself can
reach them. Adding a credentialed media reader to the shared Gitea adapter is
a separate change, deferred explicitly; the user's constraint scopes coverage
to each provider's declared capabilities.

### Player

- Every kept or generated `<video>`: `controls`, `preload="metadata"`, no
  `autoplay`, class `markdown-video`.
- Generated players carry the per-render nonce attribute (the same mechanism
  as the Shiki generated attribute) so the sanitizer distinguishes them from
  raw tags; the attribute is stripped from output.
- CSS: `display: block; max-width: 100%; max-height: 640px; height: auto`.

### Probe

- `renderMarkdownEffect` first lexes the markdown and collects standalone
  GitHub attachment URLs, then probes them through the media route with
  `Range: bytes=0-0`, at most four at a time, then renders with the outcomes.
- Each probe is an interruptible Effect: the request receives the fiber's
  `AbortSignal`, and a finalizer cancels the response body once the headers
  are read. A host that ignores Range answers 200 with the whole file, so
  the body must never be read. Interrupting the render (component teardown
  in `MarkdownHtml`) aborts its probes.
- Only settled outcomes enter the probe cache; in-flight probes are not
  shared between renders.
- Outcomes:
  - 200 or 206 with an allowed video type: player, cached.
  - `415 unsupportedMediaType`: link, cached. Only this answer proves the
    asset is not a video.
  - Anything else (400, 403, 404, 409, 429, 5xx, network failure): link,
    not cached. Credential, quota, and availability failures say nothing
    about the asset type, so the next render probes again.
- Probe cache: in-memory per media URL for the browser session.
- The rendered-HTML cache key includes the media outcome of each candidate
  (video, link, or unknown), so a render after a failed probe never reuses a
  stale result once a later probe succeeds.
- The generated API reader (`frontend/src/lib/api/runtime.ts::orvalFetch`)
  treats `video/*` responses as binary, like images, so the generated media
  client returns a `Blob` instead of decoding video bytes as text.
- Sync renders (first paint, rich-preview blocks) render links.

### Diff review thread bubbles

- `DiffReviewThreadInlineComment` renders `thread.body` through
  `MarkdownHtml` with a new `repo` prop and the timeline's
  `collapse_single_line_breaks` setting.
- `DiffFile` and `DiffRichPreview` pass the repository context.
- `DiffFile` mounts thread bubbles with Svelte `mount()` inside Pierre
  annotations, passing only the stores context today. `MarkdownHtml` reads
  the app runtime from Svelte context and throws without it. `DiffFile`
  therefore captures its full component context with `getAllContexts()`
  during initialization and passes that map to every annotation `mount()`.
  A test mounts a thread bubble through the real annotation path and checks
  that it renders markdown.
- Pierre appends each annotation wrapper to its host element's light DOM
  with a `slot` attribute (`@pierre/diffs` 1.3.5,
  `FileDiff.renderAnnotations`), so the global `.markdown-body` styles in
  `app.css` reach the bubble. The bubble wraps the rendered body in
  `.markdown-body`.

## Testing

- Go: GitHub and GitLab adapter tests against fake upstreams (Range
  forwarding, 206 passthrough, type allowlists, source scoping); route tests
  (206 headers on default and host routes, capability gating, body larger than
  25 MB streams whole, `415 unsupportedMediaType` for a non-video asset,
  `416 rangeNotSatisfiable` passthrough); fleet proxy test (streaming rule
  passes a body larger than the limit with 206 and `Content-Range`,
  non-streaming keeps the cap);
  route ownership table covers the new operations.
- Frontend Vitest: provider rules, autoplay removal, GitLab image syntax,
  Gitea and Forgejo resolution, probe outcomes and caching (415 cached; 403,
  429, and network failure probed again on the next render), probe abort on
  interruption and body cancellation on a 200 answer, diff bubble
  markdown mounted through the real Pierre annotation path.
- Real-app check: seeded app screenshots at desktop and phone width for an
  issue body video and a diff bubble video, using synthetic data only. The
  seeded backend has no real provider credential, so the seeded items must
  use sources that play without one: a raw `<video>` in a Gitea or Forgejo
  repository item (a raw tag in a GitHub item would be removed by the GitHub
  rule), or GitHub attachment URLs served by a local fake upstream if the e2e
  server can provide one. The Go tests cover the proxy path either way.

## Docs

- `context/platform-sync-invariants.md`: media proxy rules and allowlists.
- `context/fleet-architecture.md`: streaming routes.
- `context/error-handling.md`: the two new platform error codes.
- `context/inline-review-comments.md`: diff bubbles render markdown.
- One sentence in the user docs where markdown rendering is described.
