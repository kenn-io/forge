# Light Documentation Terminal Design

## Problem

The synthetic Codex transcript used in workflow screenshots always paints a
dark terminal. In a light capture, that large dark rectangle breaks the page's
visual continuity and makes the terminal look pasted in from another theme.
The live xterm terminal also stays dark today, but changing that product
behavior is separate work tracked by Kata issue `p4dy`.

## Decision

Make only the documentation overlay theme-aware. Light captures use the
approved restrained blue-gray palette: a tinted near-white terminal surface,
dark text, a muted composer surface, and accessible amber and green status
accents. Dark captures retain their current palette without visual changes.

The capture theme is passed explicitly to the synthetic transcript renderer.
The renderer selects one complete palette before creating the overlay; it does
not infer colors from screenshot names or change the real terminal component.
All transcript content, typography, spacing, dimensions, and generated asset
names remain unchanged.

## Scope

- Change the synthetic Codex overlay used by `workspace-codex-session` and
  `maintainer-overview` documentation captures.
- Preserve the existing dark overlay exactly.
- Do not change xterm, terminal settings, app theme tokens, or runtime sessions.
- Do not add a user preference or a second documentation asset format.

## Verification

Add a capture regression that fails while a light capture still uses the dark
overlay and confirms the dark capture retains its current surface. Run the
complete documentation build so both Codex screenshots pass the native-SVG
safety checks and the rendered-site WebKit comparison.
