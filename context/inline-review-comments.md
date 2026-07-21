# Inline Review Comments

Use this document for inline pull-request diff comments, local review drafts,
published review-thread ingestion, or review controls in shared diff UI.

- Staged review comments are local Middleman data. Provider clients publish a
  complete local draft; do not depend on provider-native pending drafts.
- Provider support is capability-gated per action and operation. Missing draft
  mutation, thread-read, thread-resolution, or review-action support returns the
  typed unsupported-capability path rather than enabling partial behavior.
- Bind drafts to the full provider reference, pull request number, and diff head
  SHA. Reject publish when the saved diff head is stale.
- Review ranges stay within one file and one diff side and are contiguous in
  rendered order. Full pull-request head diffs are reviewable; single-commit and
  arbitrary range diffs remain disabled until their coordinate mapping is
  explicitly supported.
- Public routes use provider-aware pull paths and internal draft/thread IDs.
  Provider review, thread, and comment IDs remain persistence metadata unless a
  concrete public API need is introduced.
- Published review parts appear in the selected pull request timeline, not as
  standalone global activity rows.
- Workspace diffs reuse the renderer with review mode disabled. They must not
  expose composers, draft trays, publish actions, or thread-resolution controls,
  and replacing a pull-request diff with workspace state clears the draft store.
