# Force-Push Lineage Restoration Design

## Problem

Strict date order collapses commits whose stable `commit_order_key` falls in an
obsolete range derived from force-push metadata. The current implementation
unions every obsolete range permanently. If a later force push restores a
previously removed lineage, the restored commits remain collapsed even though
they are current again.

The rewind behavior added on this branch is also covered only with directly
constructed component events. It needs one backend-integrated assertion that
proves SQLite-stored metadata reaches the rendered timeline correctly.

## Design

Replay force pushes in chronological event order while retaining a lineage ID
for each stable commit order and an obsolete set per lineage. A rewind from a
known `before_sha` to an earlier known `after_sha` marks only the orders in
`(after, before]` obsolete without discarding their lineage identity. A
replacement retires the active lineage and activates the lineage containing its
`after_sha`.

Restoring a previously obsolete `after_sha` clears every obsolete order in that
lineage through the restored head. Orders later than the restored head remain
obsolete, and obsolete orders belonging to other lineages are unchanged. This
preserves ancestry when a rewind and later replacement split one lineage across
multiple obsolete ranges.

## Tests

Add focused component regressions that rewind a lineage, replace it, and restore
its pre-rewind head. Every ancestor through that head must render normally even
when the rewind split the lineage into multiple obsolete ranges. A companion
case restores an intermediate head and verifies that later descendants remain
obsolete.

Add a dedicated rewind timeline to the existing SQLite-backed widgets PR #6
fixture. Store real `before_sha` and `after_sha` force-push metadata and literal,
stable `commit_order_key` values on its commits. Extend the full-stack
Playwright timeline suite to open that PR, select Strict date order, and verify
that the original lineage is fully restored while the replacement lineage is
collapsed. Using PR #6 avoids changing the existing force-push ordering
fixtures and their unrelated assertions.

## Verification

Run the focused component test through the red-green cycle, the fixture/server
tests affected by the new seed events, the targeted Chromium full-stack
Playwright case, the Svelte/type verification for the UI package, and the
repository's small-change verification checks applicable to UI plus full-stack
test changes.
