# Docs screenshots

These Playwright cases regenerate the screenshots used by the user workflow docs.
They must use the real seeded e2e backend from `cmd/e2e-server`, not mocked API
fixtures or a local developer daemon.

Run from the repository root:

```sh
node node_modules/vite-plus/bin/vp run docs:screenshots
```

The task writes SVG files under `docs/assets/generated/`. Each SVG serializes
the real app DOM and CSS into an SVG `foreignObject`; it must not embed PNG,
JPEG, or other raster screenshot payloads:

- `issue-triager-light.svg`
- `issue-triager-dark.svg`
- `code-reviewer-light.svg`
- `code-reviewer-dark.svg`

Update these captures whenever the UI changes enough that the workflow docs no
longer match the application.

Dark captures must render as dark when opened as standalone SVG files. The
capture task preserves the live root theme class and computed CSS custom
properties because the app's `:root.dark` selectors do not apply inside an SVG
`foreignObject` by themselves. Captures also wait for sync UI to return to idle
so workflow images do not show transient syncing spinners.
