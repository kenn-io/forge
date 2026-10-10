# Markdown Video Playback Design

Temporary working artifact. Never commit. Distill into `context/` docs and
delete before the PR.

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
- Range-not-satisfiable upstream answers map to a typed platform error the
  route turns into 416.
- GitHub (`platform/github`, provider wrapper, `internal/github` routed
  client): accepts the two attachment URL shapes above on the platform host,
  the old shape only for the route's own repository. Uses the user credential
  like images. Forwards only `Range`. Accepts upstream `video/mp4`,
  `video/quicktime`, `video/webm`; anything else is an invalid-argument error
  (`source`). The HTTP client has no whole-request timeout; only the wait for
  response headers is bounded.
- GitLab: accepts the same upload URLs as images; content type from the file
  extension: mp4/m4v `video/mp4`, mov `video/quicktime`, webm `video/webm`,
  ogv `video/ogg`; other extensions are invalid-argument. The foreground
  timeout bounds only the wait for response headers, not the body.
- Gitea, Forgejo, Bitbucket: no capability.

### Route

- `GET /repo/{provider}/{owner}/{name}/markdown-media?source=` and
  `GET /host/{platform_host}/repo/{provider}/{owner}/{name}/markdown-media?source=`,
  operation IDs `get-markdown-media` and `get-markdown-media-on-host`, `Range`
  request header input.
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
- For streaming rules `ProviderProxy` uses the provider-plane client's
  streaming HTTP client (no whole-request timeout, existing connect, TLS,
  header bounds) and copies status, safe headers including `Content-Range`
  and `Accept-Ranges`, and the body as it arrives instead of the 32 MB buffer.
  Non-streaming routes are unchanged.

## Frontend

### Rules per provider (markdown with a repository)

| Provider | Player source | Raw `<video>` |
| --- | --- | --- |
| GitHub | Paragraph whose only content is a bare attachment URL, after the probe confirms video | Kept only when its source or a `<source>` child is an attachment URL, rewritten to the media route; otherwise removed |
| GitLab | Image syntax with a video extension; upload URLs rewritten to the media route | Removed |
| Gitea, Forgejo | none | Kept; `attachments/<uuid>` resolved against the repository web URL, `/attachments/<uuid>` against the host; loaded directly |
| Bitbucket | none | Removed |

Markdown without a repository keeps raw `<video>` tags as today.

### Player

- Every kept or generated `<video>`: `controls`, `preload="metadata"`, no
  `autoplay`, class `markdown-video`.
- Generated players carry the per-render nonce attribute (the same mechanism
  as the Shiki generated attribute) so the sanitizer distinguishes them from
  raw tags; the attribute is stripped from output.
- CSS: `display: block; max-width: 100%; max-height: 640px; height: auto`.

### Probe

- Async render collects standalone GitHub attachment URLs and probes them
  through the media route with `Range: bytes=0-0`, bounded concurrency.
- Outcome: video type on 200/206 means player; a typed rejection (4xx) means
  link and is cached; network or 5xx failure means link, not cached.
- Probe cache: in-memory per media URL for the browser session.
- A render with any uncached failure is removed from the rendered-HTML cache
  after it resolves.
- Sync renders (first paint, rich-preview blocks) render links.

### Diff review thread bubbles

- `DiffReviewThreadInlineComment` renders `thread.body` through
  `MarkdownHtml` with a new `repo` prop and the timeline's
  `collapse_single_line_breaks` setting.
- `DiffFile` and `DiffRichPreview` pass the repository context.
- If the bubble renders inside a shadow root, it carries the markdown styles
  it needs.

## Testing

- Go: GitHub and GitLab adapter tests against fake upstreams (Range
  forwarding, 206 passthrough, type allowlists, source scoping); route tests
  (206 headers on default and host routes, capability gating, body larger than
  25 MB streams whole); fleet proxy test (streaming rule passes a body larger
  than the limit with 206 and `Content-Range`, non-streaming keeps the cap);
  route ownership table covers the new operations.
- Frontend Vitest: provider rules, autoplay removal, GitLab image syntax,
  Gitea and Forgejo resolution, probe outcomes and caching, diff bubble
  markdown.
- Real-app check: seeded app screenshots at desktop and phone width for an
  issue body video and a diff bubble video. The seeded backend has no provider
  to stream from, so the check uses a video the browser loads directly (a
  Gitea-style raw tag or a locally served file); the proxy path is covered by
  the Go tests.

## Docs

- `context/platform-sync-invariants.md`: media proxy rules and allowlists.
- `context/fleet-architecture.md`: streaming routes.
- `context/inline-review-comments.md`: diff bubbles render markdown.
- One sentence in the user docs where markdown rendering is described.
