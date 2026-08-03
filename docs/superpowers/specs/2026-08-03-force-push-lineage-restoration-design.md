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

Replay force pushes in chronological event order and maintain explicit
per-commit obsolete state keyed by stable commit order. A rewind from a known
`before_sha` to an earlier known `after_sha` marks only the orders in
`(after, before]` obsolete. When a later force push points `after_sha` into a
previously obsolete lineage, clear the restored orders between the current and
restored heads. Other replacement and missing-anchor cases retain their current
collapse semantics.

Use an explicit set rather than normalized interval algebra. Timeline event
collections are bounded, and the set makes add/remove behavior for restoration
direct and testable without introducing range-splitting edge cases.

## Tests

Add a focused component regression that performs a rewind followed by a force
push restoring the old head. In strict date order, the commits removed by the
rewind must initially be candidates for collapse but must render normally after
the restoration event is included. This test catches any implementation that
only accumulates obsolete state.

Add a dedicated rewind timeline to the existing SQLite-backed widgets PR #6
fixture. Store real `before_sha` and `after_sha` force-push metadata and literal,
stable `commit_order_key` values on its commits. Extend the full-stack
Playwright timeline suite to open that PR, select Strict date order, and verify
that only the rewound commits are collapsed while the rewind target remains
visible. Using PR #6 avoids changing the existing force-push ordering fixtures
and their unrelated assertions.

## Verification

Run the focused component test through the red-green cycle, the fixture/server
tests affected by the new seed events, the targeted Chromium full-stack
Playwright case, the Svelte/type verification for the UI package, and the
repository's small-change verification checks applicable to UI plus full-stack
test changes.
