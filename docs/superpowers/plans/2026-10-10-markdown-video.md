# Markdown Video Playback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Play provider-hosted video inline in rendered markdown (bodies, comments, timeline review comments, diff review bubbles), streamed through the daemon with the repository credential and Range support.

**Architecture:** A new `platform.MarkdownMediaReader` capability (GitHub, GitLab) opens upstream media as a stream. A Huma `StreamResponse` route copies it to the browser, and the fleet proxy streams it for spokes. The frontend markdown pipeline turns provider-approved video markup into `<video>` players and probes bare GitHub attachment URLs with `Range: bytes=0-0` to tell video from other files.

**Tech Stack:** Go, Huma v2, net/http, Svelte 5, marked, DOMPurify, Effect, Vitest.

**Spec:** `docs/superpowers/specs/2026-10-10-markdown-video-design.md`

## Global Constraints

- Allowed media types: GitHub upstream `video/mp4`, `video/quicktime`, `video/webm`; GitLab by extension mp4/m4v `video/mp4`, mov `video/quicktime`, webm `video/webm`, ogv `video/ogg` (case-insensitive).
- Forward only the `Range` request header upstream. Never buffer a media body; never cache media on disk.
- Upstream fetch bounds only the wait for response headers (30 seconds, the existing image fetch bound); the body lives until the browser disconnects.
- New platform codes: `unsupported_media_type` -> `415 unsupportedMediaType`; `range_not_satisfiable` -> `416 rangeNotSatisfiable`. Bad source shape stays `invalid_argument` -> `400 badRequest`.
- Success headers: status 200 or 206, `Content-Type`, `Content-Length` when known, `Content-Range` on 206, `Accept-Ranges: bytes`, `X-Content-Type-Options: nosniff`, `Cache-Control: private, max-age=31536000, immutable`.
- Every rendered `<video>`: `controls`, `preload="metadata"`, no `autoplay`, class `markdown-video`. CSS `display: block; max-width: 100%; max-height: 640px; height: auto`.
- Probe: `Range: bytes=0-0`, at most 4 concurrent, cache only video (2xx with allowed type) and `415`; everything else renders a link and is not cached.
- Tests use testify (Go) and synthetic names only (`acme/widgets`, `example.com`). Run `go test` with `-shuffle=on`.
- Frontend: run tools through `./node_modules/.bin/vp` (Bun only installs). Never npm.
- The spec's context-doc updates (`platform-sync-invariants`, `fleet-architecture`, `error-handling`, `inline-review-comments`) happen in the spec-consolidation step after Task 9, not inside these tasks.

## Review Focus

- A host that ignores Range answers 200 with the whole file: the probe must not read the body, and the route must still stream (not buffer) it.
- A raw GitHub `<video>` whose `<source>` child (not the `<video>` itself) carries the attachment URL must be kept and proxied; a removed raw video between two kept siblings must not disturb the siblings.
- A bare attachment URL inside a list item, blockquote, or after text on the next line is not "alone in its own paragraph" and stays a link.
- Media route on a host route (`/host/{platform_host}/repo/...`) with an uppercase extension (`DEMO.MOV`) for GitLab.
- A diff bubble mounted through Pierre after the component that created it has re-rendered still has runtime context and renders markdown.

---

### Task 1: Platform media contract

**Files:**
- Modify: `platform/client.go` (next to `MarkdownImage`), `platform/types.go:617` (Capabilities), `platform/registry.go:98` (after `MarkdownImageReader`), `platform/errors.go:17-39`
- Test: `platform/registry_test.go`

**Interfaces:**
- Produces:
  - `type MarkdownMedia struct { Body io.ReadCloser; ContentType string; ContentLength int64 /* -1 unknown */; ContentRange string; Partial bool }`
  - `type MarkdownMediaReader interface { OpenMarkdownMedia(ctx context.Context, ref RepoRef, sourceURL, byteRange string) (MarkdownMedia, error) }` (caller closes `Body`)
  - `Capabilities.ReadMarkdownMedia bool`
  - `func (r *Registry) MarkdownMediaReader(kind Kind, host string) (MarkdownMediaReader, error)` returning `UnsupportedCapability(kind, host, "read_markdown_media")` when the provider lacks the interface or flag
  - `ErrCodeUnsupportedMediaType PlatformErrorCode = "unsupported_media_type"`, `ErrCodeRangeNotSatisfiable PlatformErrorCode = "range_not_satisfiable"`

- [ ] **Step 1: Write failing tests** `TestRegistryMarkdownMediaReaderRequiresCapability`: a provider implementing `OpenMarkdownMedia` with `ReadMarkdownMedia: false` and one without the method both return an error whose `*platform.Error` has `Code == ErrCodeUnsupportedCapability` and `Capability == "read_markdown_media"`; a provider with both returns itself.
- [ ] **Step 2: Run** `go test -shuffle=on ./platform/ -run TestRegistryMarkdownMediaReader` — expect compile failure.
- [ ] **Step 3: Implement** the types, flag, registry method, and codes, mirroring `MarkdownImageReader`.
- [ ] **Step 4: Run** the same command — PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: add the platform contract for streaming markdown media"`

### Task 2: GitHub media reader

**Files:**
- Create: `platform/github/markdown_media.go`, `platform/github/markdown_media_test.go`
- Modify: `platform/github/provider.go:86-93,140-165,208-218` (client interface, Capabilities, wrapper), `internal/github/auth_router.go:531-551` (RoutedClient forwarder), `internal/testutil/serverfake/fake.go:1198,1555` (MockGH method and `OpenMarkdownMediaFn`)
- Test: `platform/github/markdown_media_test.go`, `internal/github/auth_router_test.go`

**Interfaces:**
- Consumes: Task 1 types and codes.
- Produces:
  - `func (c *Client) OpenMarkdownMedia(ctx context.Context, owner, repo, sourceURL, byteRange string) (platform.MarkdownMedia, error)`
  - `markdownMediaClient` interface in `provider.go` with that method; `Provider.Capabilities().ReadMarkdownMedia` true when the client implements it
  - `func (p *Provider) OpenMarkdownMedia(ctx context.Context, ref platform.RepoRef, sourceURL, byteRange string) (platform.MarkdownMedia, error)`
  - `func (c *RoutedClient) OpenMarkdownMedia(ctx, owner, repo, sourceURL, byteRange string) (platform.MarkdownMedia, error)`
  - `MockGH.OpenMarkdownMediaFn func(context.Context, string, string, string, string) (platform.MarkdownMedia, error)`

Accepted sources on the platform host only: `/user-attachments/assets/<id>`, and `/<owner>/<repo>/assets/<digits>/<uuid>` where owner/repo equal the route (case-insensitive). Anything else is `invalid_argument` with `Field: "source"`. Credential and HTTP client as `getAttachmentImage` (`authContext(ctx, owner, true)`, `c.source.Token`, `c.markdownImageHTTPClient`, falling back to a client with no `Timeout`). Bound only the header wait: derive a cancelable context, `time.AfterFunc(30*time.Second, cancel)`, stop the timer when headers arrive, and cancel when the returned body closes. Status mapping: 200/206 pass; 416 -> `range_not_satisfiable`; 401/403 -> `platform.PermissionDenied`; 404 -> `not_found`; else an untyped error. A disallowed `Content-Type` closes the body and returns `unsupported_media_type`.

- [ ] **Step 1: Write failing tests** against an `httptest.NewTLSServer` upstream with `platformHost` set to its host:
  - `TestOpenMarkdownMediaForwardsRangeAndReturnsPartial`: upstream asserts `r.Header.Get("Range") == "bytes=0-0"` and `Authorization` present; replies 206, `Content-Type: video/mp4`, `Content-Range: bytes 0-0/1765992`, body `"\x00"`. Assert `Partial`, `ContentRange == "bytes 0-0/1765992"`, `ContentType == "video/mp4"`, body reads `"\x00"`.
  - `TestOpenMarkdownMediaFollowsRedirectWithoutCredential`: attachment path 302s to a second TLS server, which asserts no `Authorization` and serves 206 `video/quicktime`.
  - `TestOpenMarkdownMediaRejectsImageAsset`: upstream replies `image/png` -> `*platform.Error` code `unsupported_media_type`, and the upstream body was closed (handler observes request context done).
  - `TestOpenMarkdownMediaRejectsForeignSources`: table of `https://example.com/user-attachments/assets/x`, `https://<host>/other/repo/assets/1/<uuid>`, `http://<host>/user-attachments/assets/x` -> `invalid_argument`, and the upstream receives no request.
  - `TestOpenMarkdownMediaMapsUpstreamStatus`: 416 -> `range_not_satisfiable`, 403 -> `permission_denied`, 404 -> `not_found`.
  - `TestOpenMarkdownMediaStreamsPastHeaderBound`: with the header bound shortened through an unexported package variable, upstream sends headers at once, then writes the body after the bound elapses; the read still succeeds.
  - In `internal/github/auth_router_test.go`, `TestRoutedClientAdvertisesMarkdownMedia`: the routed provider's `Capabilities().ReadMarkdownMedia` is true.
- [ ] **Step 2: Run** `go test -shuffle=on ./platform/github/ ./internal/github/ -run 'MarkdownMedia'` — FAIL.
- [ ] **Step 3: Implement** the client method, provider wrapper and capability, routed forwarder, and MockGH method.
- [ ] **Step 4: Run** the same command — PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: stream GitHub video attachments with the repository credential"`

### Task 3: GitLab media reader

**Files:**
- Create: `platform/gitlab/markdown_media.go`, `platform/gitlab/markdown_media_test.go`
- Modify: `platform/gitlab/client.go:253` (Capabilities `ReadMarkdownMedia: true`)

**Interfaces:**
- Consumes: Task 1; existing `markdownUploadParts`, `projectScopedArg`.
- Produces: `func (c *Client) OpenMarkdownMedia(ctx context.Context, ref platform.RepoRef, sourceURL, byteRange string) (platform.MarkdownMedia, error)`

Same upload URL rules and endpoint as `GetMarkdownImage`. Content type from the filename extension per Global Constraints; any other extension returns `unsupported_media_type` before any request. Keep `withForegroundTimeout` for `projectScopedArg` only; the upload request uses the same header-wait bound as Task 2 and the existing auth transport (its origin check stays). Status mapping as `GetMarkdownImage` plus 416 -> `range_not_satisfiable`; 200/206 pass through with `Content-Length` and `Content-Range`.

- [ ] **Step 1: Write failing tests** (pattern: `platform/gitlab/markdown_images_test.go`):
  - `TestOpenMarkdownMediaTypesByExtension`: table `demo.mp4`->`video/mp4`, `DEMO.MOV`->`video/quicktime`, `clip.m4v`->`video/mp4`, `clip.webm`->`video/webm`, `clip.ogv`->`video/ogg`; upstream answers `application/octet-stream`; assert `ContentType`.
  - `TestOpenMarkdownMediaRejectsNonVideoExtension`: `diagram.png` -> `unsupported_media_type`, no upstream request.
  - `TestOpenMarkdownMediaForwardsRange`: upstream asserts `Range: bytes=100-` and answers 206 with `Content-Range: bytes 100-199/200`.
  - `TestOpenMarkdownMediaMapsRangeNotSatisfiable`: 416 -> `range_not_satisfiable`.
- [ ] **Step 2: Run** `go test -shuffle=on ./platform/gitlab/ -run MarkdownMedia` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: stream GitLab video uploads named in image syntax"`

### Task 4: Media route

**Files:**
- Create: `internal/server/providerapi/markdown_media.go`, `internal/server/markdown_media_test.go`
- Modify: `internal/server/httpapi/problems.go` (codes in the const block, `allProblemCodes`, the `ProblemError.Code` enum tag, `MapPlatformError`), `internal/server/itemapi/operation_availability.go:15`, `internal/server/httpapi/repository_types.go:20`, `internal/server/httpapi/repository_resolver.go:139,314`, `internal/server/providerapi/huma_routes.go:32-41`, `internal/server/routepolicy/provider_route_policy.go:213-214`, frontend capability literals that list `read_markdown_images` (`frontend/src/lib/components/detail/PullDetail.svelte:213`, `IssueDetail.svelte:131`, `repositories/repoSummary.ts:42`, `test/mockApiFetch.ts:29`, `App.issue-routing.browser.svelte.ts:46`, `PullDetail.workflow-actions.browser.svelte.ts:43`, `App.workflow-actions.browser.svelte.ts:29`, `actions/ActionsPage.test.ts:38`), `frontend/src/lib/api/provider-routes.ts:164` (`"/markdown-media"` suffix)
- Generated: `make api-generate`

**Interfaces:**
- Consumes: Task 1 registry and codes.
- Produces:
  - Operations `get-markdown-media` (`{repoPath}/markdown-media`) and `get-markdown-media-on-host` (`{hostRepoPath}/markdown-media`), query `source`, header `Range`, `DefaultStatus: 200`, responses 200 and 206 declaring the four video media types as binary strings
  - `itemapi.CapabilityReadMarkdownMedia = "read_markdown_media"`; `RepositoryCapabilities.ReadMarkdownMedia bool \`json:"read_markdown_media"\``
  - `httpapi.CodeUnsupportedMediaType = "unsupportedMediaType"`, `httpapi.CodeRangeNotSatisfiable = "rangeNotSatisfiable"`
  - Frontend generated helpers `getGetMarkdownMediaUrl`, `getGetMarkdownMediaOnHostUrl`
  - `ProviderRouteRule.Streaming bool`; both media operations declared `ProviderHubOnly`, `ScopeProviderRead`, `Streaming: true` (Task 5 consumes the flag)

Handler: `RequireRouteCapability(..., itemapi.CapabilityReadMarkdownMedia)`, `Registry().MarkdownMediaReader(kind, host)`, open the media before returning, map errors with the existing `markdownImageError`, then return a `*huma.StreamResponse` whose body sets the success headers from Global Constraints, sets status 206 when `Partial`, copies with `io.Copy`, and always closes `Body`.

- [ ] **Step 1: Write failing tests** in `internal/server/markdown_media_test.go` with `setupTestServerWithMock` and `MockGH.OpenMarkdownMediaFn`:
  - `TestMarkdownMediaRouteStreamsPartialContent`: fn asserts `byteRange == "bytes=0-0"`, returns `Partial`, `video/mp4`, `ContentRange "bytes 0-0/10"`, length 1. Assert 206 and every success header value.
  - `TestMarkdownMediaRouteOnHostRoute`: same through `/api/v1/host/github.example.com/repo/github/acme/widgets/markdown-media` with a seeded verified repo on that host.
  - `TestMarkdownMediaRouteStreamsBodyLargerThanImageCap`: fn returns a 26 MiB generated body with `ContentLength` -1; response body length equals 26 MiB.
  - `TestMarkdownMediaRouteMapsMediaErrors`: `unsupported_media_type` -> 415 with problem code `unsupportedMediaType`; `range_not_satisfiable` -> 416 `rangeNotSatisfiable`; `invalid_argument` -> 400.
  - `TestMarkdownMediaRouteRequiresCapability`: a Bitbucket or Gitea repo route returns 409 `unsupportedCapability`.
  - Route-policy coverage stays green (`go test ./internal/server/ -run ProviderRoute`).
- [ ] **Step 2: Run** `go test -shuffle=on ./internal/server/ ./internal/server/httpapi/ -run 'MarkdownMedia|ProblemCodes|ProviderRoute'` — FAIL.
- [ ] **Step 3: Implement**, then `make api-generate`, and update the frontend capability literals with `read_markdown_media`.
- [ ] **Step 4: Run** the Go command — PASS; then `make frontend-check` — PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: serve markdown video through a streaming daemon route"`

### Task 5: Fleet streaming

**Files:**
- Modify: `internal/providerplane/client.go:62-64,344-399`, `internal/server/routepolicy/provider_proxy.go:27-84`
- Test: `internal/server/provider_proxy_test.go`

**Interfaces:**
- Consumes: Task 4 `ProviderRouteRule.Streaming`.
- Produces:
  - `type StreamingClient interface { DoStream(context.Context, federationauth.Scope, *http.Request) (*http.Response, error) }` in `providerplane`; `hubClient` implements it with `c.streamClient` and the same scope, credential, header, and body rules as `Do`
  - `ProviderProxy` for `rule.Streaming`: use `DoStream` when the client implements `StreamingClient` (else `Do`), copy filtered headers and status, then `io.Copy` the body without the `ResponseBodyLimit` buffer

- [ ] **Step 1: Write failing tests** with `newProviderProxyTestServer`:
  - `TestProviderProxyStreamsMarkdownMediaPastBodyLimit`: hub serves `/api/v1/repo/github/acme/widget/markdown-media` with 206, `Content-Range: bytes 0-33554432/40000000`, `Accept-Ranges: bytes`, and a 33 MiB body; hub asserts the forwarded `Range: bytes=0-`. Spoke response is 206 with both headers and the full body.
  - `TestProviderProxyKeepsLimitForBufferedRoutes`: the existing `/api/v1/pulls` route with a body over `ResponseBodyLimit` (set small on a directly built `ProviderProxy`) still answers 502 `upstreamError`.
  - `TestHubClientDoStreamHasNoWholeRequestTimeout`: hub sends headers, waits longer than a shortened default client timeout, then the body; `DoStream` reads it.
- [ ] **Step 2: Run** `go test -shuffle=on ./internal/server/ ./internal/providerplane/ -run 'ProviderProxy|DoStream'` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: stream markdown video from the hub to fleet spokes"`

### Task 6: Frontend player rules

**Files:**
- Modify: `frontend/src/lib/utils/markdown.ts` (renderer overrides, sanitizer hooks, allowed attrs, generated-attr stripping), `frontend/src/lib/api/runtime.ts:58` (`video/` is binary), `frontend/src/app.css:341` (video rule)
- Test: `frontend/src/lib/utils/markdown.test.ts`, `frontend/src/lib/api/runtime.test.ts` (create if absent)

**Interfaces:**
- Consumes: Task 4 URL helpers.
- Produces:
  - `export type MarkdownMediaOutcome = "video" | "link" | "unknown"`
  - `RenderMarkdownOpts.mediaOutcomes?: ReadonlyMap<string, MarkdownMediaOutcome>` keyed by the attachment source URL
  - `export function proxiedMarkdownMediaSource(source: string, repo: RepoContext): string | null` (GitHub attachment shapes and GitLab uploads only)
  - `export function githubAttachmentParagraphSources(raw: string, repo: RepoContext): string[]` returning sources of paragraphs whose only child is a bare autolink to an attachment URL (top-level paragraphs and paragraphs nested in lists or blockquotes are both "paragraph" tokens; only a paragraph whose sole inline token is that link qualifies)
  - Generated-player marker attribute `data-kenn-forge-media` carrying the render nonce, added to `MARKDOWN_ALLOWED_ATTRS` and stripped from output like `data-kenn-forge-shiki`

Rules per provider are the spec's table. Paragraph renderer: GitHub repo + qualifying paragraph + outcome `"video"` -> generated player; otherwise default. Image renderer: GitLab repo + video extension (case-insensitive, no query) -> generated player with the proxied source; otherwise default. Sanitizer: raw `<video>` (no matching nonce) kept or dropped per provider, `src` on `video`/`source` rewritten (GitHub/GitLab proxy, Gitea/Forgejo relative resolution against `https://<platformHost>/<repoPath>/` and `https://<platformHost>/`), and every kept video normalized (remove `autoplay`, set `controls`, `preload="metadata"`, add class `markdown-video`). Removing a node must not disturb DOMPurify's traversal of its siblings.

- [ ] **Step 1: Write failing tests** in `markdown.test.ts` with `renderMarkdownSync` and repo contexts for `github`, `gitlab`, `gitea`, `forgejo`, `bitbucket`, and none:
  - GitHub: `<video src="https://github.com/user-attachments/assets/a1" autoplay></video>` renders one `video.markdown-video` with `controls`, `preload="metadata"`, no `autoplay`, and `src` equal to `getGetMarkdownMediaUrl(...)` for that source; `<video controls><source src="https://github.com/user-attachments/assets/a1"></video>` keeps the video and proxies the source; `<p>a</p><video src="https://cdn.example.com/x.mp4"></video><p>b</p>` drops only the video.
  - GitHub bare URL: with `mediaOutcomes` `{source: "video"}` the paragraph renders a player; with `"link"` or absent it renders `<a href=source>`; `"text\nhttps://github.com/user-attachments/assets/a1"` renders a link even with outcome `"video"`; `githubAttachmentParagraphSources` returns the source for the standalone case and not for the inline case.
  - GitLab: `![demo](/uploads/abc/DEMO.MOV)` renders a player whose `src` is the GitLab media route for `https://gitlab.com/group/project/uploads/abc/DEMO.MOV`; `![d](/uploads/abc/diagram.png)` renders an `img`; raw `<video src=...>` is removed.
  - Gitea: `<video src="attachments/u1" controls></video>` gets `src="https://gitea.example.com/acme/widgets/attachments/u1"`; Forgejo `/attachments/u1` gets `https://codeberg.example/attachments/u1`.
  - Bitbucket: raw video removed. No repo: raw video kept with `autoplay` removed.
  - Output never contains `data-kenn-forge-media`; a raw video carrying a forged `data-kenn-forge-media` attribute is treated as raw.
  - `runtime.test.ts`: `orvalFetch` on a 206 `video/mp4` response returns a `Blob`.
- [ ] **Step 2: Run** `cd frontend && ./node_modules/.bin/vp test run src/lib/utils/markdown.test.ts src/lib/api/runtime.test.ts` — FAIL.
- [ ] **Step 3: Implement**, plus the `app.css` rule `.markdown-body video.markdown-video { display: block; max-width: 100%; max-height: 640px; height: auto; border-radius: var(--radius-sm); }`.
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: render provider-approved markdown video as a player"`

### Task 7: Frontend attachment probe

**Files:**
- Create: `frontend/src/lib/utils/markdown-media.ts`, `frontend/src/lib/utils/markdown-media.test.ts`
- Modify: `frontend/src/lib/utils/markdown.ts:620-640` (`renderMarkdownEffect`, `renderMarkdown` cache key)

**Interfaces:**
- Consumes: Task 6 `githubAttachmentParagraphSources`, `proxiedMarkdownMediaSource`, `MarkdownMediaOutcome`, `RenderMarkdownOpts.mediaOutcomes`.
- Produces:
  - `export const probeMarkdownMedia: (mediaURL: string) => Effect.Effect<MarkdownMediaOutcome>` (never fails; failures become `"unknown"`)
  - `export const resolveMarkdownMediaOutcomes: (sources: ReadonlyArray<{ source: string; mediaURL: string }>) => Effect.Effect<ReadonlyMap<string, MarkdownMediaOutcome>>` (concurrency 4, session cache keyed by `mediaURL` holding only `"video"` and `"link"`)
  - `renderMarkdownEffect` resolves outcomes for GitHub repos before rendering and passes them in `mediaOutcomes`; `renderMarkdown`'s cache key includes the outcome of each candidate

Probe: `orvalRequest(mediaURL, { signal, headers: { Range: "bytes=0-0" } })` inside `Effect.tryPromise` (the signal comes from the Effect), acquired with `Effect.acquireRelease` whose release cancels `response.body`; never read the body. 2xx with a `video/` content type -> `"video"`; 415 -> `"link"`; anything else, including network failure -> `"unknown"`.

- [ ] **Step 1: Write failing tests** in `markdown-media.test.ts` with `vi.stubGlobal("fetch", ...)` and unique URLs per test:
  - 206 `video/mp4` -> `"video"`; the stub's `Range` header is `bytes=0-0`; a second resolve for the same URL makes no request.
  - 200 `video/webm` whose body is a `ReadableStream` that records `cancel` -> `"video"` and the stream was cancelled without being read.
  - 415 problem -> `"link"` and cached; 403, 429, 502, and a rejected fetch -> `"unknown"` and the next resolve requests again.
  - Interrupting the resolving fiber aborts the request (`signal.aborted` true in the stub).
  - Six sources never have more than four requests in flight.
  - In `markdown.test.ts`: `renderMarkdown` for the same markdown with outcomes `{a: "unknown"}` then `{a: "video"}` returns a link then a player.
- [ ] **Step 2: Run** `cd frontend && ./node_modules/.bin/vp test run src/lib/utils/markdown-media.test.ts src/lib/utils/markdown.test.ts` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: tell video attachments from other files before rendering"`

### Task 8: Markdown in diff review bubbles

**Files:**
- Modify: `frontend/src/lib/components/diff/DiffReviewThreadInlineComment.svelte:17-31,200-206,345-355`, `frontend/src/lib/components/diff/DiffFile.svelte:504,512-526,675,702`, `frontend/src/lib/components/diff/DiffRichPreview.svelte:552-692`
- Test: `frontend/src/lib/components/diff/DiffReviewThreadInlineComment.test.ts`, `frontend/src/lib/components/diff/DiffFile.test.ts`

**Interfaces:**
- Consumes: `MarkdownHtml`, `RepoContext`.
- Produces: `DiffReviewThreadInlineComment` prop `repo?: RepoContext`; the body renders as `<div class="review-thread-body markdown-body"><MarkdownHtml raw={thread.body} {repo} options={{ collapseSingleLineBreaks }} /></div>`, where `collapseSingleLineBreaks` reads `getStores()?.settings?.getDetailSettings().collapse_single_line_breaks ?? false` like `EventTimeline.svelte:140`. `DiffFile` captures `getAllContexts()` during initialization and passes it as the `context` of every annotation `mount()`.

Keep `white-space: pre-wrap` off the markdown container (markdown supplies its own line breaks) and keep the idle-reply padding rule.

- [ ] **Step 1: Write failing tests:**
  - `DiffReviewThreadInlineComment.test.ts`: body `"**bold** and [link](https://example.com)"` renders a `strong` and an `a`; body containing `<video src="https://github.com/user-attachments/assets/a1"></video>` with a GitHub repo renders `video.markdown-video`.
  - `DiffFile.test.ts`: a file with one review thread annotation mounted through the Pierre path renders the bubble's markdown (`strong` present) and does not throw for missing runtime context.
- [ ] **Step 2: Run** `cd frontend && ./node_modules/.bin/vp test run src/lib/components/diff/DiffReviewThreadInlineComment.test.ts src/lib/components/diff/DiffFile.test.ts` — FAIL.
- [ ] **Step 3: Implement** and pass `repo` from `DiffFile` and `DiffRichPreview`.
- [ ] **Step 4: Run** — PASS; then the full `./node_modules/.bin/vp test run` — PASS.
- [ ] **Step 5: Commit** `git commit -m "feat: render diff review comments as markdown"`

### Task 9: Synthetic demo fixture and user docs

**Files:**
- Modify: `cmd/e2e-server/main.go` (flag, seed, media reader on `e2eWorkflowClient`), `docs/workflows/code-reviewer.md:24-26`
- Test: `cmd/e2e-server/main_test.go`

**Interfaces:**
- Consumes: Tasks 2 and 4.
- Produces: e2e-server flag `-markdown-video <path>`. When set: `e2eWorkflowClient.OpenMarkdownMedia` serves that file for `https://github.com/user-attachments/assets/e2e-demo-video` with `http.ServeContent`-equivalent Range handling (`video/mp4`) and `not_found` for other sources; seeding adds one synthetic issue and one review thread on the seeded PR whose bodies contain that URL alone in a paragraph. When unset, seeding and capabilities are unchanged.

- [ ] **Step 1: Write failing test** `TestMarkdownVideoFixtureServesRanges` in `cmd/e2e-server/main_test.go`: with a temp file of 100 bytes passed as the flag value, a request to the media route with `Range: bytes=10-19` answers 206 with those 10 bytes; without the flag, the seeded issue list does not contain the demo issue.
- [ ] **Step 2: Run** `go test -shuffle=on ./cmd/e2e-server/ -run MarkdownVideo` — FAIL.
- [ ] **Step 3: Implement**, and add one sentence to `docs/workflows/code-reviewer.md` under "Work the review": videos attached to descriptions, comments, and review comments play inline, through Forge for private repositories.
- [ ] **Step 4: Run** — PASS. Then generate a synthetic clip (`nix run 'nixpkgs#ffmpeg' -- -f lavfi -i testsrc2=size=1280x720:rate=30 -t 8 -pix_fmt yuv420p /tmp/forge-video-demo/demo.mp4`), start the e2e server with the flag and the frontend, and capture desktop and 390px-wide screenshots of the demo issue and the diff bubble; confirm the player stays inside the content width.
- [ ] **Step 5: Commit** `git commit -m "test: seed a synthetic markdown video for demos"`
