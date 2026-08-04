# Vercel Release Documentation Deployment

## Goal

Publish kenn-forge documentation at `forge.kenn.io` through a direct Vercel
build of the latest tagged release. Production follows successful releases,
while previews remain an explicit maintainer action.

## Architecture

The applied `kenn-forge-docs` Vercel project remains the hosting authority and
uses the kenn-forge repository root as its build root. The existing
`forge.kenn.io` project domain and DNS-only Cloudflare CNAME remain unchanged.
The project has no Git repository connection or Vercel GitHub App dependency.

The tagged-release workflow uploads its checkout as a source deployment with
the Vercel CLI after release publication succeeds. Repository secrets provide
the Vercel token, organization ID, and project ID; kenn-ops owns their
GitHub-encrypted values and the project connection state.

The root `vercel.json` owns the install command, build command, `site/` output,
and trailing-slash behavior. Vercel builds the same public documentation
allowlist as local development; internal plans, specifications, ADRs, reports,
and screenshot tooling never enter the rendered site.

## Remote Toolchain

Vercel's Amazon Linux install phase adds `tmux`, `nspr`, and `nss`, selects the
correct `amd64` or `arm64` Go archive, and installs Go 1.26.3 into the ignored
`.vercel/` tool directory. It also installs uv 0.12.1, materializes the Bun
workspace from the frozen lockfile, and installs Playwright Chromium. The docs
build resolves Zensical 0.0.51 explicitly instead of selecting the newest
release.

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

The first verified cold preview completed on Vercel's 4-core, 8 GB Amazon
Linux builder in about three minutes. Later previews restore Vercel's build
cache, but the install remains correct without it and stays bounded by the
platform build timeout.

## Local and Remote Workflows

`make docs-build` remains the ordinary local verification path and may use the
existing `go run` e2e-server behavior. `make docs-vercel-build` reproduces the
remote build order after the Vercel install script has populated dependencies
and local tools.

`.github/workflows/release.yml` deploys production only after its release job
succeeds, using the exact tagged checkout that produced the downloadable
binaries. The manual preview target provides on-demand source previews without
PR comments or deployment statuses from a GitHub App. The manual production
target remains an operator escape hatch for a tagged checkout. All three paths
run the same Vercel install and build commands.

## Failure Handling

Installation fails for unsupported CPU architectures, missing Amazon Linux
packages, tool downloads, frozen-lockfile drift, or Chromium installation
errors. The build fails for frontend errors, Go compilation errors, screenshot
capture errors, Zensical errors, or rendered-site browser failures. A failed
preview or production build never falls back to stale pages or screenshots.

Missing or invalid Vercel credentials fail the deployment job without changing
the existing production deployment. Vercel credentials are unavailable to
ordinary CI and are consumed only by the release deployment step.

## Verification

A focused unit test proves that `PLAYWRIGHT_E2E_SERVER_BINARY` selects the
explicit executable and omits `go run` arguments. Script tests continue to
exercise the public-source staging boundary. Workflow lint verifies the
release-job dependency and Vercel CLI invocation. A local `docs-vercel-build`
run, when the Linux dependencies are available, proves the full frontend,
binary, screenshot, Zensical, and rendered-site sequence.

## Rollout

1. Apply the reviewed kenn-ops change that removes the Git connection and
   provisions the encrypted GitHub Actions secrets.
2. Merge the kenn-forge direct-build and release-workflow configuration.
3. Confirm an on-demand preview completes the direct screenshot build.
4. Confirm the next successful tagged release updates `forge.kenn.io`.

No resource replacement, DNS migration, generated-asset branch, prebuilt
deployment path, or Vercel GitHub App is part of this change.
