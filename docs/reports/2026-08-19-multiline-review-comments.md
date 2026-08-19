# Multiline review comments research

## Recommendation

Implement issue [#936](https://github.com/kenn-io/forge/issues/936) in Forge's diff UI using the line-selection and gutter-utility APIs already present in the pinned `@pierre/diffs` 1.3.5. No Pierre upgrade, fork, or upstream change is required for the first implementation.

The intended interaction should be:

1. Drag across line numbers, or click a line number and Shift-click another, to select a range.
2. Use the gutter `+` action to open the inline composer for that selection.
3. Limit the range to one file, one diff side, and one hunk. If the provider does not advertise native multiline ranges, retain single-line commenting.

This matches Pierre's own DiffsHub integration: it enables line selection and the gutter utility, uses selection completion separately from comment creation, and receives the selected range through `onGutterUtilityClick` ([source](https://github.com/pierrecomputer/pierre/blob/59ec35ffac97abccef4c69f8d58d3747cbfbc6cb/apps/diffshub/components/DiffsHubViewer.tsx#L427-L455)).

## Why this is mostly a Forge frontend change

Forge already carries multiline ranges through the GitHub review path:

- `DiffFile.svelte` converts a Pierre `SelectedLineRange` into `start_side`, `start_line`, `side`, and `line`, and downgrades invalid selections to one line. Its checks already require provider support, the same side, and the same hunk ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/frontend/src/lib/components/diff/DiffFile.svelte#L339-L412)).
- GitHub advertises `NativeMultilineRanges: true` ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/internal/github/sync.go#L2003-L2032)).
- GitHub publishing maps the stored start fields to `DraftReviewComment.StartSide` and `StartLine` ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/internal/github/sync.go#L3350-L3439)). GitHub's review endpoint accepts `comments[].line`, `side`, `start_line`, and `start_side` for multiline comments ([official API](https://docs.github.com/en/rest/pulls/reviews#create-a-review-for-a-pull-request)).
- The API validates that start fields are paired, positive, ordered, and allowed by the provider capability ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/internal/server/pullapi/diff_review_handlers.go#L1090-L1121), [capability check](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/internal/server/pullapi/diff_review_handlers.go#L551-L570)). The database query layer also persists `start_side` and `start_line` ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/internal/db/queries_review.go#L121-L139)).
- Existing frontend tests exercise capability-gated Shift selection, same-hunk enforcement, and saved range rendering ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/frontend/src/lib/components/diff/DiffFile.test.ts#L732-L755), [same-hunk case](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/frontend/src/lib/components/diff/DiffFile.test.ts#L930-L1002)).

The missing piece is the normal user interaction. `PierreFileDiff.svelte` currently passes `enableLineSelection: false` and disables Pierre's line hover treatment ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/frontend/src/lib/components/diff/PierreFileDiff.svelte#L192-L200)). It then walks Pierre's shadow DOM and injects a custom `+` button for every rendered line ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/frontend/src/lib/components/diff/PierreFileDiff.svelte#L1265-L1391)). That implementation supports a range only through the non-obvious sequence "click `+`, then Shift-click another `+`"; it does not expose Pierre's drag selection.

## What Pierre 1.3.5 already provides

Forge pins `@pierre/diffs` 1.3.5 ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/frontend/package.json#L28-L35)), corresponding to Pierre tag `diffs-v1.3.5` at commit [`59ec35f`](https://github.com/pierrecomputer/pierre/tree/59ec35ffac97abccef4c69f8d58d3747cbfbc6cb).

That exact release provides:

- `enableLineSelection`, with click, drag, and Shift-extension on line numbers ([official example](https://github.com/pierrecomputer/pierre/blob/59ec35ffac97abccef4c69f8d58d3747cbfbc6cb/apps/docs/app/%28diffs%29/_examples/LineSelection/LineSelection.tsx#L38-L50)).
- A `SelectedLineRange` containing start/end line numbers and sides ([source](https://github.com/pierrecomputer/pierre/blob/59ec35ffac97abccef4c69f8d58d3747cbfbc6cb/packages/diffs/src/types.ts#L521-L529)).
- Selection lifecycle callbacks, controlled selection, and `FileDiff.setSelectedLines` ([callbacks](https://github.com/pierrecomputer/pierre/blob/59ec35ffac97abccef4c69f8d58d3747cbfbc6cb/packages/diffs/src/managers/InteractionManager.ts#L217-L239), [example](https://github.com/pierrecomputer/pierre/blob/59ec35ffac97abccef4c69f8d58d3747cbfbc6cb/apps/docs/app/%28diffs%29/_examples/LineSelection/LineSelection.tsx#L164-L179)).
- `enableGutterUtility` and `onGutterUtilityClick(range)`, which are the supported equivalents of Forge's injected buttons ([source](https://github.com/pierrecomputer/pierre/blob/59ec35ffac97abccef4c69f8d58d3747cbfbc6cb/packages/diffs/src/managers/InteractionManager.ts#L217-L239)).
- Upstream browser coverage for drag selection ([source](https://github.com/pierrecomputer/pierre/blob/59ec35ffac97abccef4c69f8d58d3747cbfbc6cb/packages/diffs/test/e2e/line-select.pw.ts#L32-L56)).

Pierre's line selection starts only from the line-number column ([source](https://github.com/pierrecomputer/pierre/blob/59ec35ffac97abccef4c69f8d58d3747cbfbc6cb/packages/diffs/src/managers/InteractionManager.ts#L807-L816)), so enabling it does not restore row-wide content clicks. That distinction matters because Forge previously disabled row-wide selection after diff-content clicks unexpectedly opened the composer ([Forge change](https://github.com/kenn-io/forge/commit/ceed3408208fcd5ab39ab7730b1af9446ebb3a0b)).

## Proposed implementation seams

Keep provider rules and range normalization in `DiffFile.svelte`; keep Pierre-specific interaction wiring in `PierreFileDiff.svelte`.

In `PierreFileDiff.svelte`:

- Pass the component's `enableLineSelection` prop through to Pierre instead of hardcoding `false`.
- Enable Pierre's gutter utility whenever inline review comments are enabled.
- Expose selection-end and gutter-utility callbacks to the parent. Selection-end updates the visible selection; gutter activation opens the composer for the normalized range.
- Remove `applyLineCommentButtons` and its custom shadow-DOM button, pointer snapshot, and Shift-range helpers once equivalent behavior is covered through Pierre's callbacks.

In `DiffFile.svelte`:

- Split the current `handlePierreSelection` responsibility into "normalize/store selection" and "open composer." This avoids creating or moving the composer repeatedly while a pointer drag is in progress.
- Continue to use `normalizedSelection` as the authority for capability, side, and hunk constraints.
- Keep the current `diffHeadSHA` requirement so a draft remains bound to the reviewed head.
- On cancellation, clear both the composer and Pierre selection, matching the current single-line behavior.

This change should not require a new API type, database migration, generated client update, or GitHub backend change.

## Alternatives considered

### Extend the injected `+` buttons

Forge could add pointer-drag handling around the existing custom buttons. This is the smallest conceptual change, but it would duplicate interaction logic Pierre already owns and deepen the dependency on undocumented shadow-DOM structure. It also leaves the range gesture difficult to discover. Not recommended.

### Enable native selection but retain the injected buttons

This is a reasonable temporary rollout if replacing the gutter utility exposes a Pierre defect. It delivers drag selection with less UI churn, but leaves two interaction systems coordinating selection state and retains the most brittle code. Use only as a fallback, not the target design.

### Upgrade, patch, or fork Pierre first

The pinned release already exposes the required APIs and Pierre uses them in its own review UI, so an upstream prerequisite is not supported by the current evidence. Open an upstream issue only for a reproduced 1.3.5 defect, especially around Forge's virtualized diffs, slotted annotations, or context expansion. Any such report should include a minimal Pierre reproduction rather than Forge's shadow-DOM integration.

## Verification plan

Add focused coverage for the user-visible interaction, not for Pierre's library internals:

- Unified and split views: forward and backward drag on line numbers, followed by the gutter `+`, opens one composer spanning the expected lines.
- Shift-click extends an existing selection and the gutter action uses the full range.
- Cross-side and cross-hunk attempts normalize to one valid line; providers without `native_multiline_ranges` continue to create single-line drafts.
- A saved range renders across all selected lines; cancel clears the transient selection; editing and submitting preserve `start_side` and `start_line`.
- Selection still works after context expansion and while files enter and leave the virtualized viewport, with existing inline annotations present.
- Keyboard activation of the gutter action remains available. Touch behavior should be verified on an actual touch-capable browser before claiming support; Pierre uses pointer events, but this research did not test Forge on touch hardware.

The highest-risk area is not GitHub serialization. It is keeping Pierre's internal selection, Forge's controlled selection, annotation insertion, and virtualized rerenders synchronized. A browser-level test should cover that combined path after the component tests pass.

## Other providers

Treat other providers as follow-up work rather than part of issue #936's GitHub request.

- GitLab's discussions API supports `position[line_range]`, including start and end positions ([official API](https://docs.gitlab.com/api/discussions/#parameters-for-multiline-comments)). Forge already builds a `LineRangeOptions`, but currently advertises `NativeMultilineRanges: false` ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/internal/platform/gitlab/client.go#L244-L267), [range mapping](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/internal/platform/gitlab/diff_review.go#L187-L204)). GitLab requires endpoint `line_code` values, so the capability should remain false until those are populated and tested.
- Forgejo's API model exposes `extra_lines_count` for review comments ([official OpenAPI](https://codeberg.org/swagger.v1.json)), while Forge currently advertises multiline ranges as false ([source](https://github.com/kenn-io/forge/blob/4976043d38a485f0b61704d0e945c43c670d6c52/internal/platform/forgejo/client.go#L139-L154)). Its semantics and supported server versions need a separate provider test before enabling the capability.

## Decision summary

The backend and provider model are already ready for GitHub multiline review ranges. Implement the feature by adopting Pierre 1.3.5's supported line-selection and gutter-utility APIs, with Forge retaining ownership of provider capability checks and valid range normalization. Do not make Pierre upstream work a prerequisite; use it only for defects reproduced during the integration.
