# Rich Preview Review Card Anchoring Design

## Context

Markdown rich preview needs to show review cards in the rendered document, not only in the raw source diff. The first implementation made cards visible, but it anchored them by splitting Markdown into raw line fragments and rendering each fragment independently. That breaks normal Markdown semantics for constructs that depend on whole-document context, such as loose lists, reference links, and future parser extensions.

The fix should preserve rendered Markdown behavior first, then layer review-card placement and diff cues on top.

## Goals

- Show inline review cards inside Markdown rich preview for unified and split diff modes.
- Preserve whole-document Markdown parsing semantics.
- Keep review placement logic out of `DiffRichPreview.svelte` except for rendering an explicit model.
- Avoid per-word green underlines for newly added block content.
- Keep unanchored, file-level, stale-head, or uncertain review threads visible in a clearly separated fallback area.

## Non-Goals

- Do not build a custom Markdown parser.
- Do not make non-Markdown binary, image, PDF, or plain text previews line-anchor review cards.
- Do not change the source-diff review annotation behavior.
- Do not promise whole-file Markdown semantics when the diff view only has hunk lines. Rich preview preserves whole-document semantics for the reconstructed hunk document it renders.

## Architecture

Introduce a small Markdown rich-preview model layer near the existing Markdown utilities. The model owns source-line mapping, Markdown rendering, block diffing, and review placement inputs. `DiffRichPreview.svelte` consumes the model and renders it.

The component should no longer split raw Markdown by blank lines or code fences. It should not call `renderMarkdown()` on arbitrary fragments as a placement strategy.

The model has two explicit boundaries:

- The reconstructed hunk document is the complete Markdown input available to rich preview. It is made from the diff hunk lines that belong to each side.
- The render block is a top-level Marked token/container derived from that hunk document. Blocks are display and anchoring units; they are not separate Markdown documents with independent parser semantics.

The model should expose block records with this minimum shape:

```ts
type MarkdownPreviewBlock = {
  key: string;
  oldStart?: number;
  oldEnd?: number;
  newStart?: number;
  newEnd?: number;
  unifiedHtml: string;
  beforeHtml?: string;
  afterHtml?: string;
};
```

`DiffRichPreview.svelte` renders:

- unified mode: `block.unifiedHtml`, followed by review cards assigned to that block;
- split mode: `block.beforeHtml` and `block.afterHtml`, followed by review cards assigned to the matching old or new side;
- fallback review threads in a separated stack before the block stream, not as fake inline anchors.

The render block is the top-level Markdown token/container produced by the parser. This keeps lists, tables, blockquotes, HTML blocks, and other structured containers valid. A comment on a list item, table row, or nested blockquote child anchors after the containing top-level rendered block rather than inserting a review card inside invalid list/table markup. The review card header keeps the exact file line reference so the line-level target remains visible even when the valid DOM insertion point is the container boundary.

This is intentional: preserving valid rendered Markdown takes priority over placing cards inside structured containers.

## Data Flow

1. Build old and new Markdown documents from diff hunk lines.
2. While building those documents, keep a generated-line to source-line map for old and new sides.
3. Parse old and new Markdown as whole hunk documents through the canonical Markdown parser path.
4. Use Marked's lexer token stream and a deterministic generated-line cursor to derive top-level token source spans. For each token, use `token.raw` line counts to advance the cursor; ignore non-rendered `space` and `def` tokens as visible blocks while still counting their lines. Map generated lines back to source old/new line numbers through the side-specific line maps.
5. Render blocks using the canonical renderer while preserving the same Markdown semantics as whole-document rendering. If that cannot be guaranteed for a construct, do not decompose that construct into independently rendered fragments.
6. Diff corresponding old/new rendered blocks for unified HTML.
7. Project the same diff into before/after HTML for split mode.
8. Assign each review thread to the block whose exact old or new mapped source-line set contains the target line. Start/end ranges are display metadata only; they are not enough for anchoring when a block spans multiple hunks with hidden source gaps.

If a block cannot be mapped confidently, keep the rendered Markdown correct and leave related review threads in the fallback area. Correct rendering is more important than guessed inline placement.

`renderMarkdownBlocks(raw, repo)` is the only visible block-rendering API this feature should use. It must parse with the configured Marked instance, keep document-level Marked behavior such as in-document reference definitions intact for the visible block output, render only visible top-level tokens as blocks, and sanitize every block through the same DOMPurify allow-list used by `renderMarkdown()`. `extractMarkdownDefinitionLines(raw, repo)` is an approved companion parser-context API for the synthetic-separator stripping path; no other rich-preview code should create an ad hoc definition scanner or alternate sanitizer. Reference-link resolution inside the reconstructed hunk document is a required regression test for this API boundary.

Multiple hunks are joined in backend hunk order with an explicit parser-only separator: blank line, thematic break line `---`, blank line. The separator prevents accidental list/table merging across unrelated hunks and can influence Markdown parsing the same way a thematic break does, but it is hidden from rendered preview output. Synthetic separator tokens have no old/new source-line mapping, are excluded from mapped source-line sets and display range calculations, do not participate in block alignment, and cannot receive review cards. Hunk boundaries are not source continuity hints: when Marked emits separate list, blockquote, table, or paragraph tokens on either side of the hidden separator, render those as separate top-level blocks and do not merge them by text shape. If Marked absorbs synthetic separator lines into a mapped multi-line token, such as a fenced code block spanning hunks, the rendered block is rebuilt from mapped source lines only so the synthetic lines are stripped before sanitization and diffing. That stripped re-render must include in-document reference definitions from the reconstructed hunk document as parser context; if a future token type cannot be stripped without preserving document-level semantics, keep the correct rendered output and move the uncertain block's related cards to fallback instead of guessing. User-authored `---` lines inside a hunk keep their source mapping and render normally. Definitions inside the reconstructed hunk document may resolve across that separator; definitions outside loaded hunks remain unavailable.

Repeated paragraphs, headings, or list items are resolved by source order, not rendered text identity alone. The generated-line cursor makes identical text in different source locations produce distinct ranges.

Definitions outside the loaded diff hunks are outside this feature's available input. They may not resolve until the preview is backed by full-file content. Definitions inside the reconstructed hunk document must keep working.

Deleted-only comments anchor against old-side mapped line sets. Added/current comments anchor against new-side mapped line sets. Multi-line review ranges use the same target side and line that the source diff uses for card placement; if the target range crosses multiple top-level Markdown containers, the card anchors to the first containing top-level block for that target line. If no containing block exists, or if the target line falls inside a hidden hunk gap within a block's display start/end range, the card becomes a file-level fallback card rather than guessing.

## Visual Behavior

Rich preview should read like rendered Markdown first.

For added or removed block-level content, use quiet block background and border styling. Avoid underlining every word in newly inserted paragraphs, headings, or list items. Inline `ins` and `del` styling should be reserved for small text changes inside otherwise matching blocks.

Split mode uses the before/after pane and block background as the primary block-level add/delete cue. Inline `ins` and `del` nodes inside otherwise matching split-pane blocks keep a subtle add/delete background cue, but never use native underline or strike-through text decoration. This no-decoration rule applies to block-level diff wrappers and their descendants; block fill and pane placement carry the add/delete signal for full-block changes.

Review cards should sit between rendered blocks without introducing artificial paragraph breaks, isolated list fragments, or source-diff-style clutter.

For structured containers, the card sits after the whole top-level container. It must not become a child of `<ul>`, `<ol>`, `<table>`, `<tbody>`, `<tr>`, `<blockquote>`, or similar elements unless the implementation has a valid, tested container-specific insertion model.

List-item anchoring is the only container-specific exception in this milestone. The pure rich-preview builder accepts explicit old-side and new-side source-line sets that need internal anchors. It keeps normal lists as one rendered list unless one of those target lines falls inside the list. When a review targets a context list item, the component uses that target line as the anchor boundary. When a review targets an added or deleted list item, it also adds a same-indent comparable context list item as an alignment boundary so unchanged sibling items do not render as false additions or deletions. Boundary selection prefers the nearest preceding comparable item, then the nearest following comparable item; if neither exists, the changed side can still split for card placement and the opposite side remains absent or whole.

Split list blocks are a placement device, not new Markdown paragraph breaks. They render only the minimum number of `<ul>` or `<ol>` chunks needed to put review cards after their target items; untargeted consecutive items stay grouped in the same chunk. Split chunks must preserve numbering for ordered lists, keep nested lists and multiline item content inside the owning item, abandon splitting when rendered `<li>` counts do not match source markers, and use styling that removes only synthetic wrapper margins. Untargeted lists, loose lists outside targeted regions, tables, and blockquotes remain whole top-level blocks. Targeted loose lists use the same source-marker grouping, preserving each rendered `<li>` subtree; if the renderer/source marker count no longer matches, the card anchors after the whole list container instead of guessing. Added/deleted list boundary scans treat indented continuation lines and pre-blank lazy continuation lines as part of the current list item; they stop at lower-indented content, lower-indented markers, or same-indent non-list text after a blank line so unrelated blocks are not selected as alignment context.

Side-by-side rich preview is a comparison surface, not a single prose column. It should use the available file width for its two panes, while unified rich preview keeps the narrower readable Markdown measure.

Wrapper elements around rendered blocks are acceptable only when they preserve the readable Markdown layout. They must not force one-off margin, table, list, or heading behavior that differs materially from the normal rich preview. Layout regressions for lists, headings, paragraphs, and tables should be covered by component or browser assertions when wrappers change.

## Performance

Markdown rich preview should avoid unbounded quadratic work. Block alignment may use an LCS only below a fixed block-product threshold; above that threshold it must fall back to a coarse delete/insert projection or another bounded strategy. Parsing, token mapping, sanitization, exact-line membership checks, and block diffing should be derived from stable inputs so Svelte recomputation only happens when the file, repository context, or relevant review-thread inputs change. If review-thread placement becomes a hot path for large blocks, precompute per-block line sets rather than scanning large arrays repeatedly.

## Acceptance Criteria

- Rich preview does not render arbitrary raw Markdown fragments independently.
- Reference-style links defined inside the rendered hunk document still resolve.
- List items emit separate rich-preview anchor blocks only when explicit review target lines require item-level placement, and untargeted consecutive items stay grouped instead of splitting into artificial one-line lists.
- Split list anchors do not introduce synthetic vertical margins or blank lines beyond the review card itself.
- Separate lists and tables on opposite sides of a hidden hunk separator stay separate top-level blocks.
- Review cards on repeated text blocks map by source line order.
- Review cards on structured container children remain near the valid top-level container boundary and keep the exact line reference in the card header.
- File-level, stale-head, and unmapped review threads remain visible in a separated fallback stack.
- Split rich preview anchors cards to the matching old or new side rather than dumping all cards above the preview.
- Block-level additions and deletions use block diff styling without per-word underline decoration.
- Split-pane inline text changes retain a visible add/delete background cue without underline or strike-through decoration.
- Split-pane rich preview uses the available file width instead of the unified preview's prose-width cap.
- The rendered DOM remains valid for lists, tables, and blockquotes.
- Large Markdown diffs do not allocate an unbounded block comparison matrix.
- The block-rendering path preserves centralized Markdown sanitization and the allowed attribute policy.
- Multi-hunk previews do not show artificial `<hr>` or separator content that was not part of the reviewed file.
- Synthetic separator lines are hidden even when a Markdown token spans hunks, while user-authored thematic breaks remain visible.
- Review cards do not anchor to hidden source gaps between hunks, even when one rendered block spans both hunks.

## Testing

Add unit coverage for the rich-preview model:

- reference-style links still resolve when review cards are anchored;
- untargeted lists remain whole rendered lists;
- list items expose separate rich-preview anchor blocks when split-line inputs target individual source items;
- ordered-list numbering, nested lists, multiline items, targeted loose lists, and mismatched source/rendered item counts have focused coverage;
- multi-hunk fenced code, HTML blocks, blockquotes, lists, and tables do not expose synthetic separator lines and keep the expected top-level structure;
- stripped spanning-token re-renders keep in-document reference definitions available through the approved parser-context API;
- user-authored thematic breaks inside hunks still render;
- mapped source-line sets exclude synthetic separator lines and hidden hunk gaps;
- review threads assign to old-side and new-side blocks;
- uncertain or file-level threads fall back visibly.

Keep component coverage for:

- unified rich preview renders review cards inside the preview after their target block;
- split rich preview renders review cards near the matching side block;
- added and deleted list-item review cards keep unchanged sibling items aligned rather than rendering them as false additions or deletions;
- added and deleted list-item review cards at the first and last list item keep unchanged sibling items aligned;
- review threads targeting hidden hunk gaps remain file-level fallback cards;
- source diff review annotations remain unchanged.

Keep browser e2e coverage for the PR files page rich preview toggle, including review-card cases that prove cards are not at the top of the file, multiple cards render in source order, list-item cards attach after the matching rendered item, split-list anchors do not add synthetic list margins, and hidden-gap review threads remain file-level fallback cards.

## Implementation Staging

1. Define the pure rich-preview model contract and fixtures.
2. Add failing model tests for reference definitions, loose lists, block ranges, structured container anchoring, and large-block fallback.
3. Implement generated-line to source-line mapping from Marked tokens through the shared block-rendering API.
4. Wire unified rich preview to the model with component coverage.
5. Wire split rich preview to the same model with component coverage.
6. Adjust block-level diff styling.
7. Add full PR files Playwright coverage before finalizing the branch.

## Implementation Boundaries

- The first implementation should replace the fragment-rendering path, not polish it.
- The Markdown rich-preview model should be pure enough to test without Svelte.
- The component should receive a simple render model and avoid knowing parser details.
- Any fallback should be explicit and tested, not silent semantic drift.
- Sanitization must stay centralized through the existing Markdown utility policy; no new unsanitized token/block HTML path is allowed.
