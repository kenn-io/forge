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
8. Assign each review thread to the block whose old or new source range contains the target line.

If a block cannot be mapped confidently, keep the rendered Markdown correct and leave related review threads in the fallback area. Correct rendering is more important than guessed inline placement.

Repeated paragraphs, headings, or list items are resolved by source order, not rendered text identity alone. The generated-line cursor makes identical text in different source locations produce distinct ranges.

Definitions outside the loaded diff hunks are outside this feature's available input. They may not resolve until the preview is backed by full-file content. Definitions inside the reconstructed hunk document must keep working.

## Visual Behavior

Rich preview should read like rendered Markdown first.

For added or removed block-level content, use quiet block background and border styling. Avoid underlining every word in newly inserted paragraphs, headings, or list items. Inline `ins` and `del` styling should be reserved for small text changes inside otherwise matching blocks.

Review cards should sit between rendered blocks without introducing artificial paragraph breaks, isolated list fragments, or source-diff-style clutter.

For structured containers, the card sits after the whole top-level container. It must not become a child of `<ul>`, `<ol>`, `<table>`, `<tbody>`, `<tr>`, `<blockquote>`, or similar elements unless the implementation has a valid, tested container-specific insertion model.

## Acceptance Criteria

- Rich preview does not render arbitrary raw Markdown fragments independently.
- Reference-style links defined inside the rendered hunk document still resolve.
- Loose lists that Marked treats as one list remain one rendered list.
- Review cards on repeated text blocks map by source line order.
- Review cards on structured container children remain near the valid top-level container boundary and keep the exact line reference in the card header.
- File-level, stale-head, and unmapped review threads remain visible in a separated fallback stack.
- Split rich preview anchors cards to the matching old or new side rather than dumping all cards above the preview.
- Block-level additions and deletions use block diff styling without per-word underline decoration.
- The rendered DOM remains valid for lists, tables, and blockquotes.

## Testing

Add unit coverage for the rich-preview model:

- reference-style links still resolve when review cards are anchored;
- loose lists render as a single list where Markdown defines one;
- review threads assign to old-side and new-side blocks;
- uncertain or file-level threads fall back visibly.

Keep component coverage for:

- unified rich preview renders review cards inside the preview after their target block;
- split rich preview renders review cards near the matching side block;
- source diff review annotations remain unchanged.

Keep browser e2e coverage for the PR files page rich preview toggle, including a review-card case that proves cards are not at the top of the file.

## Implementation Staging

1. Add pure model tests and a `markdown-rich-preview` utility.
2. Implement generated-line to source-line mapping from Marked tokens.
3. Wire unified rich preview to the model.
4. Wire split rich preview to the same model.
5. Adjust block-level diff styling.
6. Add component and Playwright coverage.

## Implementation Boundaries

- The first implementation should replace the fragment-rendering path, not polish it.
- The Markdown rich-preview model should be pure enough to test without Svelte.
- The component should receive a simple render model and avoid knowing parser details.
- Any fallback should be explicit and tested, not silent semantic drift.
