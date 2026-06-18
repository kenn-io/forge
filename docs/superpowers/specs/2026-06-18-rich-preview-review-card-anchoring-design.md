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

## Data Flow

1. Build old and new Markdown documents from diff hunk lines.
2. While building those documents, keep a generated-line to source-line map for old and new sides.
3. Parse old and new Markdown as whole documents through the canonical Markdown parser path.
4. Derive top-level token or rendered-block source spans from parser metadata and the generated-line maps.
5. Render blocks using the canonical renderer while preserving the same Markdown semantics as whole-document rendering. If that cannot be guaranteed for a construct, do not decompose that construct into independently rendered fragments.
6. Diff corresponding old/new rendered blocks for unified HTML.
7. Project the same diff into before/after HTML for split mode.
8. Assign each review thread to the block whose old or new source range contains the target line.

If a block cannot be mapped confidently, keep the rendered Markdown correct and leave related review threads in the fallback area. Correct rendering is more important than guessed inline placement.

## Visual Behavior

Rich preview should read like rendered Markdown first.

For added or removed block-level content, use quiet block background and border styling. Avoid underlining every word in newly inserted paragraphs, headings, or list items. Inline `ins` and `del` styling should be reserved for small text changes inside otherwise matching blocks.

Review cards should sit between rendered blocks without introducing artificial paragraph breaks, isolated list fragments, or source-diff-style clutter.

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

## Implementation Boundaries

- The first implementation should replace the fragment-rendering path, not polish it.
- The Markdown rich-preview model should be pure enough to test without Svelte.
- The component should receive a simple render model and avoid knowing parser details.
- Any fallback should be explicit and tested, not silent semantic drift.
