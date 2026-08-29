# Documentation Authoring

Use this document for changes to user-facing documentation, workflow
screenshots, or the Zensical site.

## Documentation Ownership

- `context/` owns current internal architecture, cross-cutting invariants, and
  maintainer constraints; update or remove each rule with its implementation.
- Staged Zensical pages under `docs/`, including `docs/workflows/`, own current
  user and maintainer workflows, not internal architecture or API detail.
- Promote rationale to an ADR only when it explains a constraint that still
  binds current code and is not derivable from it; reports retain reproducible
  outcomes.
- Superpowers plans and design specs are temporary working artifacts, not repository
  documentation. Before committing, distill current contracts into the matching
  `context/` topic docs, delete the artifacts, and do not convert them into ADRs.
- Verify candidate documentation against implementation and tests before
  promoting it into living documentation.
- Review substantial docs changes as rendered before-and-after blocks; source
  diffs miss navigation, theme, and generated screenshot changes.
  (`skills/docs-diff-review/SKILL.md`)

## Authoring Conventions

- Keep user guidance concise and workflow-oriented: describe UI capabilities
  and maintainer workflows without overexplaining internals. Treat the HTTP API
  as an internal or thin-client concern rather than regular user guidance.
- In published docs and marketing prose, introduce the product by its full
  title-cased name once per page, then say Forge; reserve `kenn-forge` for the
  binary, CLI, config, and path references inside code spans. Internal docs
  (`context/`, ADRs, reports, README) keep using `kenn-forge`.
  (`scripts/check-docs-branding.mjs`)
- The public site is tiered: hand-written static marketing pages in `website/`
  (`/` pitch page, `/guide/` visual tour; dark-only, JetBrains Mono headings,
  ember accent) lead into the Zensical docs under `/docs/`. Position the
  product as an integrated agent workspace with local-first code forge sync,
  not a maintainer-only console.
- Every published docs page ships as a rendered/markdown pair (`/docs/<page>/`
  plus `/docs/<page>.md`; index as `/docs.md`) listed in the hand-maintained
  `docs/llms.txt`, and the build fails when any half or listing is missing —
  a new page must be added to the file, nav, allowlist, and `llms.txt`.
  (`scripts/build-docs.mjs::verifySiteRoot`)
- User-facing workflow screenshots are generated into a staged docs tree by the
  docs build and must not be tracked in Git. Playwright captures in
  `docs/screenshots/` use the real seeded e2e backend, not mocked API fixtures
  or a developer daemon.
- Generated workflow screenshots must use native SVG geometry, not XHTML in
  `foreignObject`; WebKit clips responsive `foreignObject` images.
  (`docs/screenshots/docs-screenshots.spec.ts::nativeSVGSnapshot`)
- Static Codex terminal overlays reproduce a one-off real Codex TUI capture,
  including its composer and model/path status; sanitize local paths to the
  public synthetic repository. (`docs/screenshots/docs-screenshots.spec.ts::embedSyntheticCodexTranscript`)
- Roborev workflow captures use a synthetic loopback daemon through the
  isolated server's real proxy, never an installed daemon or database.
  (`docs/screenshots/docs-screenshots.spec.ts::startSyntheticRoborevDaemon`)
- Vercel deployments build the complete site from the repository root. The
  remote build must compile `cmd/e2e-server` only after the frontend has been
  copied into `internal/web/dist`, then pass the prebuilt binary through
  `PLAYWRIGHT_E2E_SERVER_BINARY` so screenshot readiness excludes Go compile
  time. (`scripts/vercel-build-docs.sh`)
- Vercel restricts rendered-site checks to Chromium because its runtime lacks
  Playwright WebKit dependencies; the browser-image docs CI lane runs Chromium
  and WebKit. (`scripts/vercel-build-docs.sh`, `.github/workflows/ci.yml::docs`)
- Production docs use a default-branch `workflow_run`; the released SHA must be
  on `main` and latest before build and before/after promotion. A stale attempt
  dispatches trusted latest-release reconciliation. Promotion uses Vercel's
  project endpoint so project-scoped tokens avoid the CLI's user lookup. No
  Vercel Git app or GitHub environment. (`.github/workflows/deploy-docs.yml`)
- Download links retain a static GitHub Releases fallback and enhance to the
  matching `browser_download_url` returned by the latest-release API because a
  release or the API may be unavailable. Automatic platform selection occurs
  only when the browser exposes an unambiguous OS and architecture; otherwise
  the Releases fallback and explicit Quick Start artifact choices remain.
  (`docs/overrides/main.html::assetFor`)
- Verify screenshot asset-path findings against rendered `site/` output because
  Zensical can rewrite raw HTML paths when `use_directory_urls` is enabled.
- Zensical resolves `docs_dir` and `site_dir` relative to the config file. To
  build `docs/zensical.toml`, stage a scratch project root containing the config
  beside a copy of `docs/`, then run `uvx zensical build` there.
