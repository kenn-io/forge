# Documentation Authoring

Use this document for changes to user-facing documentation, workflow
screenshots, or the Zensical site.

## Documentation Ownership

- `context/` owns maintainer decisions and cross-cutting invariants, not
  descriptions of how the current code works.
- Staged Zensical pages under `docs/`, including `docs/workflows/`, own current
  user and maintainer workflows, not internal architecture or API detail.
- Promote rationale to an ADR only when it explains a constraint that still
  binds current code and is not derivable from it; reports retain reproducible
  outcomes.
- Verify candidate documentation against implementation and tests before
  promoting it into living documentation.
- Devbox App registration is a one-time manual GitHub workflow documented
  generically in `docs/devboxes.md`; private deployments follow that guide
  rather than adding registration commands or a separate setup flow.
- The public devbox guide must stand alone for outside operators, without
  depending on a private provisioning repository or exposing its infrastructure.
- Review substantial docs changes as rendered before-and-after blocks; source
  diffs miss navigation, theme, and generated screenshot changes
  (`skills/docs-diff-review/SKILL.md`).

## Authoring Conventions

- User-facing docs are concise and workflow-oriented: state UI capabilities and
  the maintainer workflows Forge enables. The HTTP API is internal, not user
  guidance.
- In published docs and marketing prose, introduce the product by its full
  title-cased name once per page, then say Forge; reserve `kenn-forge` for
  binary, CLI, config, and path references in code spans. Internal docs keep
  `kenn-forge` (`scripts/check-docs-branding.mjs`).
- The public site is tiered: static marketing pages in `website/` (dark-only,
  JetBrains Mono headings, ember accent) lead into the Zensical docs under
  `/docs/`.
- Position the product as an integrated agent workspace with local-first code
  forge sync, not a maintainer-only console.
- Every published docs page ships as a rendered/markdown pair listed in the
  hand-maintained `docs/llms.txt`, and the build fails when either half or the
  listing is missing (`scripts/build-docs.mjs::verifySiteRoot`).
- A new page must be added to the nav, `publishedFiles`, `llms.txt`, and
  `.vercelignore`; local builds read the whole checkout, so only the script
  test catches a page Vercel never receives.

## Screenshots

- Generate workflow screenshots only with `make docs-screenshots`, against the
  real seeded e2e backend, never mocks or a developer daemon. Keep captures
  untracked on `main`; docs builds sync only a complete pinned set.
- Publish each complete screenshot set at the root of the orphan `docs-assets`
  branch as a fast-forward child of its tip, and pin its full SHA in
  `scripts/docs-assets.ref`. Never force-push: released docs pin earlier SHAs.
- Generated workflow screenshots use native SVG geometry, not XHTML in
  `foreignObject`, because WebKit clips responsive `foreignObject` images.
- Static Codex terminal overlays reproduce a one-off real Codex TUI capture;
  sanitize local paths to the public synthetic repository.

## Build And Deploy

- Stage Zensical input from an explicit public allowlist; internal plans,
  specs, ADRs, reports, and screenshot tooling never enter the staged tree or
  rendered `site/`.
- Zensical resolves `docs_dir`/`site_dir` relative to its config file, so build
  from a scratch root holding copies of `docs/zensical.toml` and `docs/`.
- Check screenshot asset-path findings against rendered `site/` output;
  `use_directory_urls` rewrites raw HTML paths.
- Vercel builds are static docs builds; Go, the application frontend,
  screenshot capture, and browser verification stay out of them
  (`scripts/vercel-build-docs.sh`).
- Direct Vercel CLI deployments use root `.vercelignore`, not `.gitignore`, as
  a public-input allowlist of static docs, website, favicon, and build scripts.
- Rendered-site browser verification is separate from the static build and
  runs in Chromium and WebKit.
- Production docs deploy when a stable GitHub Release is published; the
  released SHA must be on `main` and still the latest release before build and
  around promotion (`.github/workflows/deploy-docs.yml`).
- A stale deploy attempt refuses and dispatches trusted latest-release
  reconciliation, so production always converges on the latest release.
- Promotion uses Vercel's project endpoint so project-scoped tokens avoid the
  CLI's user lookup. There is no Vercel Git app or GitHub environment.
- Download links keep a static GitHub Releases fallback, since a release or the
  API may be unavailable, and enhance to the matching latest-release asset.
- Pick a download platform automatically only when the browser exposes an
  unambiguous OS and architecture; otherwise keep the Releases fallback and
  explicit Quick Start choices.
