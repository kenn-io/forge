# Vercel Git Documentation Deployment

## Goal

Publish kenn-forge documentation at `forge.kenn.io` through a direct Vercel build.
Pull requests receive previews and pushes to `main` receive production
deployments without a GitHub Actions deployment job or Vercel credentials in
GitHub.

## Architecture

The applied `kenn-forge-docs` Vercel project remains the hosting authority and
uses the kenn-forge repository root as its build root. The existing
`forge.kenn.io` project domain and DNS-only Cloudflare CNAME remain unchanged.

The root `vercel.json` owns the install command, build command, `site/` output,
and trailing-slash behavior. Vercel builds the same public documentation
allowlist as local development; internal plans, specifications, ADRs, reports,
and screenshot tooling never enter the rendered site.

## Remote Toolchain

Vercel's Amazon Linux install phase adds `tmux`, `nspr`, and `nss`, selects the
correct `amd64` or `arm64` Go archive, and installs Go 1.26.3 into the ignored
`.vercel/` tool directory. It also installs `uv`, materializes the Bun
workspace from the frozen lockfile, and installs Playwright Chromium.

The build phase prepends the repository-local Go and uv directories to
`PATH`. It does not mutate a developer toolchain or depend on tools installed
outside the checkout.

## Generated Screenshots

Every Vercel deployment generates the workflow screenshots from the real
seeded e2e backend. There is no generated-asset branch, checked-in screenshot
fallback, or prebuilt deployment artifact.

The short Vercel build command delegates to `scripts/vercel-build-docs.sh`.
That wrapper first compiles the frontend and copies it into
`internal/web/dist`. Only then does it compile `cmd/e2e-server`, ensuring the
binary embeds the current SPA. `PLAYWRIGHT_E2E_SERVER_BINARY` points the
screenshot harness at that prebuilt binary, so the harness's readiness timeout
measures server startup rather than a cold `go run` compile.

After screenshot capture, the build runs Zensical from the staged project and
executes the rendered-site Chromium checks before copying verified output to
`site/`.

## Local and Remote Workflows

`make docs-build` remains the ordinary local verification path and may use the
existing `go run` e2e-server behavior. `make docs-vercel-build` reproduces the
remote build order after the Vercel install script has populated dependencies
and local tools.

Vercel automatically builds pull-request commits and `main`. The manual
preview and production Make targets remain operator escape hatches that submit
ordinary source deployments, allowing Vercel to run the same install and build
commands as Git-triggered deployments.

## Failure Handling

Installation fails for unsupported CPU architectures, missing Amazon Linux
packages, tool downloads, frozen-lockfile drift, or Chromium installation
errors. The build fails for frontend errors, Go compilation errors, screenshot
capture errors, Zensical errors, or rendered-site browser failures. A failed
preview or production build never falls back to stale pages or screenshots.

The Vercel Git connection requires the Vercel GitHub integration to have access
to the kenn-forge repository. Infrastructure configuration reports that provider
error without introducing a compatibility path or moving deployment
credentials into GitHub Actions.

## Verification

A focused unit test proves that `PLAYWRIGHT_E2E_SERVER_BINARY` selects the
explicit executable and omits `go run` arguments. Script tests continue to
exercise the public-source staging boundary. A local `docs-vercel-build` run,
when the Linux dependencies are available, proves the full frontend, binary,
screenshot, Zensical, and rendered-site sequence.

## Rollout

1. Merge the kenn-forge direct-build configuration.
2. Apply the Vercel project configuration that connects the repository root and
   selects `main` as the production branch.
3. Confirm a preview completes the direct screenshot build.
4. Confirm the first production deployment serves `forge.kenn.io`.

No resource replacement, DNS migration, generated-asset branch, prebuilt
deployment path, or GitHub Actions deployment secret is part of this change.
