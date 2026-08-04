# Docs screenshots

The documentation build uses these Playwright cases to generate the screenshots
used by the user workflow docs. They use the real seeded e2e backend from
`cmd/e2e-server`, not mocked API fixtures or a local developer daemon.

Run from the repository root:

```sh
node scripts/build-docs.mjs
```

The build stages the docs in a temporary directory, writes the generated SVGs
there, and then builds the complete site into `site/`. Generated screenshots
are build output and are not tracked in Git.

Each SVG serializes the real app DOM and CSS into an SVG `foreignObject`; it
must not embed PNG, JPEG, or other raster screenshot payloads:

- `issue-triager-light.svg`
- `issue-triager-dark.svg`
- `code-reviewer-light.svg`
- `code-reviewer-dark.svg`
- `maintainer-overview-light.svg`
- `maintainer-overview-dark.svg`
- `first-run-light.svg`
- `first-run-dark.svg`
- `code-reviewer-agent-launch-light.svg`
- `code-reviewer-agent-launch-dark.svg`
- `workspace-codex-session-light.svg`
- `workspace-codex-session-dark.svg`

Workflow captures use a configured seeded server. First-run captures use a
second isolated server with its repositories removed and a synthetic tooling
response. Neither path reads a developer config, database, or running daemon.
The workspace cases configure a synthetic Codex target backed by an isolated
long-running shell. They never start the installed Codex binary or read agent
credentials.

The maintainer overview opens the seeded pull request from Activity, hosts its
ready workspace in the detail layout, and selects the running Codex session
before serialization.

Dark captures must render as dark when opened as standalone SVG files. The
capture task preserves the live root theme class and computed CSS custom
properties because the app's `:root.dark` selectors do not apply inside an SVG
`foreignObject` by themselves. Captures also wait for sync UI to return to idle
so workflow images do not show transient syncing spinners.
