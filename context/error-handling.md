# Error Handling

Use this document for changes that touch HTTP API failure responses, platform
error translation, generated API clients, or frontend behavior that branches on
server errors. Retry and scheduling policy lives in
[`context/retries-and-backoffs.md`](./retries-and-backoffs.md).

## API Problem Envelope

- API failures are RFC 9457 `application/problem+json` with stable `code` and
  `details` extension members (`internal/server/httpapi/problems.go::ProblemError`).
- `title` and `detail` are prose and may change; UI, clients, tests, and
  automation branch only on `code` and `details`.
- Huma's `errors[]` is reserved for Huma validation compatibility; never add new
  machine-readable contracts there.

## Code Taxonomy

`internal/server/httpapi/problems.go::ProblemCode` is the complete wire-code list.

- Wire codes are camelCase; platform error constants stay snake_case and are
  translated only at the `internal/server` boundary.
- Add a code only when a frontend or API client needs a distinct recovery branch;
  keep the OpenAPI enum stable and run `make api-generate` after changing it.
- Use the most specific domain not-found code available, not generic `notFound`.
- A provider lookup that reports the item moved returns `notFound` with
  destination provider, host, owner, and name so clients can retarget.
- `workspaceNotFound` does not prove the workspace was deleted.
- `validationError` names the request field; capability gates use
  `unsupportedCapability` and never hide unsupported mutations behind GitHub-only behavior.
- `rateLimited` carries `details.retryAfter` as UTC RFC3339 whenever the provider
  tracker or platform error exposes one.
- `branchConflict` puts branch names in top-level `details`; when the occupied
  deterministic path discriminator is true, clients must not offer an alternate branch.
- `resyncRequired` means a provider item lacks stable external identity for a safe
  mutation; clients must get a successful resync before retrying.
- `unsupportedMediaType` is the only media rejection that proves an asset is not a
  video, so only it may be remembered by clients.
- Archive report overflow is `payloadTooLarge` with `details.reason = "reportTooLarge"`;
  clients branch on the reason, not prose.
- `hubUnavailable` (503) is retryable; provider reads and writes must never fall back
  to spoke-local provider tables.
- A stale manual-workflow definition is `conflict` with reason
  `workflow_definition_changed`; clients must reload the catalog
  (`internal/server/workflowapi/routes.go::Handler.dispatch`).
- Mid-stack merges fail closed on immediate and deferred paths unless explicitly
  enabled: `conflict` with reason `mid_stack_merge_disallowed`
  (`internal/server/pullapi/routes.go::Handler.requireMidStackMergeAllowed`).

### Non-idempotent provider writes

- After dispatching a non-idempotent provider write, keep only typed errors that
  prove rejection; every unverified failure, including local persistence failure
  after provider success, is `mutationOutcomeUnknown`
  (`internal/server/httpapi/problems.go::ProviderMutationProblem`).
- Clients fence related writes on `mutationOutcomeUnknown`, reconcile fresh
  authority, and never replay the request automatically.
- A spoke proxying a provider write to the hub also returns `mutationOutcomeUnknown`
  when the hub response is unreadable or oversized
  (`internal/server/routepolicy/provider_proxy.go::ProviderProxy.ServeHTTP`).

### Head-bound conflicts and stale approvals

- Head-bound mutations return `conflict` with a stable `details.reason`:
  `stale_state`, `conflict`, or `head_unknown`; suggestion apply adds `not_open` and
  `head_repo_unknown`, both failing closed with no branch mutation.
- A workspace branch pull with nothing to pull is reason `in_sync`; bulk pulls count
  it as up to date, not a failure.
- On hard-head-binding providers only `stale_state` triggers a server-side resync;
  resyncing on other conflicts would persist a head nobody reviewed.
- Providers without hard binding also resync on generic merge conflicts to refresh
  the local mergeable view.
- A missing synced head disables head-bound actions in first-party clients with no
  request; the head arrives through normal detail load or sync.
- `stale_state` makes the client reload detail and prompt re-review.
- Server `head_unknown` never syncs server-side; recovery is a client reload, with
  actions disabled until the response carries `platform_head_sha`.
- When a `stale_state` failure leaves a provider side effect behind, `details.context`
  carries it and `detail` repeats it; clients show it so users know a blind retry
  duplicates the side effect.
- Stale-approval revocation members (`revocation`, `review_id`, `cause`) appear only
  when an approval was actually created upstream; clients treat them as optional.
- An approval whose head cannot be verified after submit must not stand: it is
  revoked like a moved head, with `cause` distinguishing `head_unverifiable`.
- A review-draft publish that is both partial and stale returns the 409 `stale_state`
  envelope, not `partially_published`; published local draft copies are cleared first.

## Server Construction

- All Huma services, including standalone devbox services, build failures with
  `internal/server/httpapi` constructors, never direct `huma.Error4xx`/`Error5xx`.
- Huma uses JSON v2: nil slices serialize as `[]`; use a pointer or nullable wrapper
  only when `null` has domain meaning (`internal/server/httpapi/problems.go::init`).
- Optional numeric or boolean fields whose absence carries meaning need `omitzero`;
  JSON v2 `omitempty` keeps zero and false (`internal/gitclone/types.go::Line`).
- Map domain failures to statuses with `errors.Is`/`errors.As`, never `err.Error()`
  text; wording changes otherwise collapse a documented 4xx into 500.

## Platform Error Translation

- Cancellation and deadline errors pass through only when the request context is
  done; a provider child-context deadline is `502 upstreamError`
  (`internal/server/providerapi/markdown_images.go::markdownImageError`).

## Frontend Handling

- Behavior branches on the typed `code`; components may display `detail` only as text.
- Development API failures must stay inspectable after a toast disappears; body
  capture is opt-in outside process-compose and goes to local logs, never analytics
  (`frontend/src/lib/dev/apiFailureLogging.ts::apiFailureLogging`).
- On a federation spoke, hub reachability rides the existing browser event stream,
  not a second EventSource, and is distinct from local live-update connectivity.
- A hub disconnect marks provider data unavailable immediately; reconnection
  refreshes provider projections before clearing that state.
- A failed projection refresh with a successful sync-status probe means the hub is
  available and the error is reported separately; a failed probe keeps data unavailable.
- A stale replay cursor follows the same reconnect rule, but cached hub state for a known
  outage suppresses the provider refetch.
- During a hub outage provider pages must not present stale cached state as current,
  while local workspace navigation stays available
  (`frontend/src/lib/app-stores.svelte.ts::reconcileProviderState`).

## Tests

- API error contracts use wire-level server tests with real SQLite asserting status,
  content type, `code`, and relevant `details`, never prose unless prose is the feature.
