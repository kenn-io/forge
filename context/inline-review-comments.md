# Inline Review Comments

Use this document for inline pull-request diff comments, local review drafts,
published review-thread ingestion, or review controls in shared diff UI.

- Staged review comments are hub-owned kenn-forge data; spokes forward draft reads, writes, and
  publication so a fleet has one draft and conflict domain. Do not depend on provider-native pending
  drafts (`internal/db/queries_review.go::DB.GetOrCreateMRReviewDraft`).
- Before a standalone daemon becomes a spoke, its draft is handed to the hub by stable repository ID and
  pull number; identical retries return the same receipt, and divergent hub state is a blocking conflict
  that overwrites neither side (`internal/db/queries_provider_state_handoff.go::DB.ImportProviderReviewDraft`).
- Unsupported draft mutations, thread resolutions, and review actions return typed capability errors;
  thread ingestion is skipped when reads are unavailable.
- Bind drafts to the full provider reference, pull request number, and diff head SHA; reject publish
  when the saved diff head is stale (`internal/server/pullapi/diff_review_handlers.go::Handler.publishDiffReviewDraft`).
- Known-stale published line threads stay visible after the rendered file and never attach by numeric
  coincidence; current or head-unknown threads stay inline, and file-level comments stay at the file
  header (`frontend/src/lib/components/diff/review-thread-context.ts::reviewThreadSnapshotState`).
- Review ranges stay within one file and one diff side and are contiguous in rendered order. Only full
  pull-request head diffs are reviewable until single-commit and range coordinate mapping is supported
  (`internal/server/pullapi/diff_review_handlers.go::dbReviewLineRange`).
- Review thread bubbles render markdown with the timeline's line-break setting and the pull request's
  repository, so provider video rules apply.
- Pierre annotations mount outside the Svelte tree; pass the captured `getAllContexts()` to every
  annotation `mount()`, because markdown reads the app runtime from context
  (`frontend/src/lib/components/diff/DiffFile.svelte`).
- Line-number pointer and keyboard selection only change the active review range; the gutter utility
  opens the composer (`frontend/src/lib/components/diff/DiffFile.svelte::handlePierreGutterUtility`).
- Keep unsaved composer text above renderer-owned annotation mounts: context expansion can remount the
  composer without changing its review range (`frontend/src/lib/components/diff/DiffFile.svelte::composerBody`).
- Public routes use provider-aware pull paths and internal draft/thread IDs; provider review, thread,
  and comment IDs stay persistence metadata unless a concrete public API need arises
  (`internal/server/pullapi/handler.go::registerReviewDraftRoutes`).
- Published review parts appear in the selected pull request timeline, not as standalone global
  activity rows (`internal/server/pullapi/diff_review_handlers.go::Handler.ingestDiffReviewThreads`).
- Provider moderation metadata lives on both thread rows and their timeline events and is committed
  atomically, so neither can clear or bypass it independently.
- Provider publication is irreversible; follow-up thread-ingestion failures preserve the (partially)
  published response and schedule reconciliation.
- Workspace diffs reuse the renderer with review mode disabled: no composers, draft trays, publish
  actions, or thread-resolution controls, and replacing a pull-request diff with workspace state clears
  the draft store (`frontend/src/lib/components/diff/DiffView.svelte::reviewEnabled`).
