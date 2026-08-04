# Vercel Git Documentation Deployment

## Goal

Publish Forge documentation at `forge.kenn.io` through the same remote-build
model used by Roborev and AgentsView. Pull requests receive Vercel previews,
and pushes to `main` receive production deployments without a GitHub Actions
deployment job or Vercel credentials in GitHub.

## Architecture

The applied `kenn-forge-docs` Vercel project remains the hosting authority.
OpenTofu updates that project in place to connect `kenn-io/forge`, use `main`
as the production branch, and set `docs/` as the project root. The existing
`forge.kenn.io` project domain and DNS-only Cloudflare CNAME remain unchanged.

`docs/vercel.json` owns the remote install, build, output, and trailing-slash
settings. Vercel installs the pinned Zensical toolchain with `uv`, runs a docs
build wrapper, and publishes `docs/site/`. The build stages only the explicit
public documentation allowlist; internal plans, specifications, ADRs, reports,
and screenshot tooling never enter the rendered site.

## Generated Screenshots

The Vercel builder does not run Forge's Go, tmux, and Playwright screenshot
stack. Maintainers generate the twelve workflow SVGs locally from the real
seeded e2e backend and publish them to the force-updated orphan branch
`docs-generated-assets`, matching the reference projects.

The remote build fetches that branch, validates the exact expected asset
manifest, and hydrates `docs/assets/generated/` before staging the public site.
Generated screenshots remain ignored on the main branch. Asset regeneration is
maintainer-triggered; Vercel Git integration remains responsible only for docs
source previews and production builds.

## Local and Remote Workflows

`make docs-build` remains the full local verification path: regenerate
screenshots, build Zensical, and run rendered-site browser checks. Separate
asset-branch targets generate, validate, and optionally push the orphan asset
commit without switching the maintainer's current branch.

Vercel automatically builds pull-request commits and `main`. The existing
manual preview and production Make targets remain available as operator escape
hatches and invoke ordinary remote Vercel builds, not prebuilt uploads.

## Failure Handling

Remote builds fail when the asset branch cannot be fetched, any expected SVG is
missing, an unexpected asset is present, the pinned toolchain cannot install,
or Zensical rejects the staged public site. A failed preview or production
build never falls back to stale checked-in screenshots.

The Vercel Git connection requires the Vercel GitHub integration to have access
to `kenn-io/forge`. OpenTofu reports that provider error without creating a
compatibility path or moving deployment credentials into GitHub Actions.

## Verification

Script tests exercise public-source staging, strict asset hydration, and orphan
branch creation using temporary repositories. The full local docs build proves
all screenshot captures, Zensical output, and rendered-site browser behavior.
Mocked OpenTofu tests preserve the repository connection, production branch,
project root, custom domain, and DNS-only CNAME contract.

## Rollout

1. Merge the Forge deployment changes and seed `docs-generated-assets` with all
   twelve expected SVGs.
2. Apply the updated `kenn-ops` Vercel project configuration, which connects
   the repository and starts Git-triggered builds against complete `main`.
3. Confirm the first production deployment serves `forge.kenn.io`.

No resource replacement, DNS migration, or GitHub Actions deployment secret is
part of this change.
