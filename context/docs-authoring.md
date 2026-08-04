# Documentation Authoring

Use this document for changes to user-facing documentation, workflow
screenshots, or the Zensical site.

- Keep user guidance concise and workflow-oriented: describe UI capabilities
  and maintainer workflows without overexplaining internals. Treat the HTTP API
  as an internal or thin-client concern rather than regular user guidance.
- Always write the product name as `kenn-forge` in documentation and prose;
  never title-case it.
- User-facing workflow screenshots are generated into a staged docs tree by the
  docs build and must not be tracked in Git. Playwright captures in
  `docs/screenshots/` use the real seeded e2e backend, not mocked API fixtures
  or a developer daemon.
- Vercel deployments build the complete site from the repository root. The
  remote build must compile `cmd/e2e-server` only after the frontend has been
  copied into `internal/web/dist`, then pass the prebuilt binary through
  `PLAYWRIGHT_E2E_SERVER_BINARY` so screenshot readiness excludes Go compile
  time. (`scripts/vercel-build-docs.sh`)
- Download links retain a static GitHub Releases fallback and enhance to the
  matching `browser_download_url` returned by the latest-release API because a
  release or the API may be unavailable. (`docs/overrides/main.html::assetFor`)
- Verify screenshot asset-path findings against rendered `site/` output because
  Zensical can rewrite raw HTML paths when `use_directory_urls` is enabled.
- Zensical resolves `docs_dir` and `site_dir` relative to the config file. To
  build `docs/zensical.toml`, stage a scratch project root containing the config
  beside a copy of `docs/`, then run `uvx zensical build` there.
