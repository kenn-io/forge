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

Each capture prints the stabilized Chromium page to a one-page vector PDF.
Poppler's `pdftocairo` converts that page into native SVG paths, clips,
gradients, and embedded icon data so responsive images render consistently in
WebKit. Install Poppler before running the build (`brew install poppler` on
macOS or the `poppler-utils` package on Linux).

The generated files are:

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
before export.

The visible Codex pane contains a short static transcript derived from a
one-time real Codex run in a synthetic widget-cache repository. The capture
harness injects the sanitized text into the terminal DOM before printing. Its
prompt composer and model/path status reproduce the same captured Codex TUI
with the temporary path replaced by the public synthetic repository path. Docs
builds never run Codex or read agent credentials. This synthetic overlay
follows the capture's light or dark theme; theme-aware live terminals are a
separate product concern.

Dark captures print with the active screen theme. Before export, the task waits
for sync UI to return to idle and rejects transient syncing labels or private
temporary paths. The generated SVG contract rejects `foreignObject`, scripts,
remote asset URLs, and private build paths; the rendered-site suite also checks
responsive painting in iPhone-sized WebKit.
