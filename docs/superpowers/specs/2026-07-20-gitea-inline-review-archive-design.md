# Gitea Inline Review and Gitealike Archive Coverage Design

## Goal

Complete Gitea inline review-thread ingestion through the canonical merge-request detail sync path, and report inline review comments as supported archive data for the validated Forgejo and Gitea versions. Archive hydration remains a thin caller of ordinary item sync.

## Support Boundary

Middleman will advertise Gitea review-thread reads and archive inline-comment coverage for Gitea 1.24.6 or newer. This matches the oldest version exercised by the repository's Gitea container fixture and avoids claiming support for ancient releases merely because the SDK endpoint existed there.

The Gitea client will retain the server-version result established during SDK client construction and derive the capability decision from a `>= 1.24.6` constraint. Older Gitea instances will continue to construct normally when otherwise supported, but `ReadReviewThreads` and `Archive.InlineReviewComments` will remain false. Existing capability gates will then return typed unsupported-capability results instead of attempting a partial read.

Forgejo's existing review-thread reader remains enabled. Once its current container fixture proves the complete read path, its archive capabilities will also advertise inline review comments.

## Provider Architecture

Gitea will receive a provider-specific review reader parallel to the existing Forgejo implementation:

1. List every page of pull-request reviews using the SDK's explicit page and page-size options.
2. Fetch every inline comment exposed by each review-comment endpoint.
3. Normalize each comment into one `platform.MergeRequestReviewThread`.
4. Return the complete dataset to `syncProviderMRReviewThreads`.

The normalized record will preserve provider review, thread, and comment IDs; author; body; direct URL; created and updated times; path; side and line; old/new line representation; commit SHA; and resolution state when the provider supplies it. Repository identity remains attached by the existing persistence boundary using the full provider, host, owner, and repository reference.

No new archive transport, normalizer, or persistence method will be introduced. The existing flow remains:

`archive hydration -> SyncArchiveItem -> regular merge-request detail sync -> syncProviderMRReviewThreads -> revision-fenced dataset commit`

## Capability Reporting

Gitea's client-level `Capabilities` method will extend the shared gitealike capabilities only when its version constraint passes:

- `ReadReviewThreads = true`
- `Archive.InlineReviewComments = true`

Forgejo's client-level `Capabilities` method will add `Archive.InlineReviewComments = true` beside its existing `ReadReviewThreads = true` declaration.

The shared gitealike provider will not advertise review-thread reads by default. This keeps provider differences behind their concrete client capability declarations and prevents transports without proven complete readers from inheriting support accidentally.

## Error Handling and Completeness

Review-list or comment-list failures abort the reader and leave the prior revision-fenced dataset intact. The reader never returns a successful partial dataset. HTTP failures use each provider's existing error mapping.

Review pagination uses the platform's cycle-detecting collector and drains until the provider reports exhaustion. Hydration imposes no review-page, review-count, or inline-comment-count cap: it fetches every review and every comment before the revision-fenced transaction replaces the prior complete dataset. Context cancellation, a cursor cycle, or any provider error aborts the read without publishing a partial dataset. The per-review comments endpoint is treated as a complete response because the pinned SDK exposes no pagination input for that endpoint. If a future provider version introduces paginated review comments, support must not be broadened until the transport can consume every page.

## Testing

Provider tests will cover:

- Gitea capability behavior above and below the 1.24.6 floor.
- Gitea review pagination across multiple review pages.
- Gitea normalization of right-side and left-side comments, identity fields, URLs, timestamps, commit SHAs, and resolution state.
- Failure behavior that rejects partial datasets.
- Forgejo and Gitea archive inline-comment capability declarations.

The existing Forgejo and Gitea container fixtures will create pull requests with inline review comments. Their integration tests will exercise both ordinary merge-request detail sync and archive hydration, then verify:

- persisted review-thread rows and their normalized fields;
- archive coverage reports inline comments as supported;
- archive progress and report counts include the persisted comments;
- both hydration entry points use the canonical sync path.

The default Gitea container remains pinned at 1.24.6, making that version the executable compatibility floor. Forgejo remains pinned to the repository's existing validated container line.

## Non-Goals

- Supporting Gitea versions older than 1.24.6 for inline review ingestion.
- Adding inline review draft creation to Gitea.
- Adding archive-specific review fetching or persistence.
- Introducing a cross-provider compatibility shim or GitHub-derived fallback.
