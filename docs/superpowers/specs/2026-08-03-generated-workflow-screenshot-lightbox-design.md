# Generated workflow screenshot lightbox design

## Goal

Make every generated workflow screenshot easy to inspect at full size while
keeping ordinary documentation images unchanged. The first screenshot on the
home page must demonstrate the complete maintainer workflow: Activity open, a
pull request selected, its live workspace selected, and a coding-agent session
visible.

## Scope

The feature applies only to figures marked `workflow-shot`. Those figures use
the light/dark SVGs produced during each docs build. It does not add a general
Markdown image lightbox, a JavaScript dependency, or tracked screenshot assets.

## Interaction design

The visible theme-specific image is a button-like zoom target. Activating it by
mouse or keyboard opens a native `<dialog>` containing the same generated SVG
at its natural, inspectable size. The dialog provides an explicit Close button,
closes when the user clicks the backdrop or presses Escape, and restores focus
to the image that opened it. The dialog uses the active light or dark image, so
changing themes does not expose the hidden variant.

A visually hidden dialog heading supplies the modal's accessible name through
`aria-labelledby`, so assistive technology receives context as focus moves.

The image keeps its existing alt text. Its zoom target has an accessible name
that announces the inspection action, and the close control has a stable
accessible name. If native dialog support is unavailable, the page keeps the
normal inline image rather than failing documentation navigation.

## Generated screenshot composition

The `maintainer-overview` light and dark captures remain the first home-page
figure. Before navigating to Activity, the isolated seeded e2e backend creates
the pull-request workspace and launches its synthetic Codex session. The
capture then opens Activity, selects the seeded “Add widget caching layer” pull
request, selects its ready workspace, and exposes the Codex session in the
workflow layout. Assertions verify the selected pull request, live workspace,
and visible coding-agent session before serialization.

All data stays synthetic and the SVGs remain generated inside the staged docs
tree during the Vercel build.

### Coding-agent transcript

Author the terminal content once by running the maintainer's real Codex CLI in
an isolated synthetic repository against a small widget-cache task. Sanitize
the useful excerpt into generic static fixture text, then make the synthetic
Codex session print that frozen transcript before its long-running wait. The
checked-in fixture must contain no real repository names, paths, credentials,
session identifiers, or user data. Local and Vercel screenshot builds never
start Codex or require agent credentials.

## Implementation boundaries

- Keep figure markup in the published Markdown and styling in
  `docs/stylesheets/extra.css`.
- Put the small shared dialog behavior in the Zensical override so it applies
  after client-side navigation as well as initial page load.
- Reuse the existing isolated workspace setup and selectors in
  `docs/screenshots/docs-screenshots.spec.ts`; do not introduce a mocked API or
  a developer daemon.
- Keep the one-time Codex authoring run outside the docs build; only its
  sanitized static transcript becomes a screenshot fixture.
- Preserve theme switching, image alt text, captions, generated asset paths,
  and the public-docs allowlist.

## Verification

Rendered-site Playwright coverage must prove that only a `workflow-shot` image
opens the dialog, the active theme image is shown, Close/backdrop/Escape close
it, and focus returns to the opener. With `showModal` disabled before page
initialization, the same suite must prove the original theme-specific image
stays visible without a trigger or dialog. The generated screenshot suite must prove
the first capture contains Activity, the selected pull request, the selected
ready workspace, and the Codex session in both themes.

Run the docs script tests, all 12 generated screenshot cases, the complete
rendered-site suite, and the final Vercel build. Inspect the first light and dark
SVGs and the deployed lightbox before publishing.
