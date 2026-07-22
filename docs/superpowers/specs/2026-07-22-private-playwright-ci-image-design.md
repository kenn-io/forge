# Private Playwright CI Image Design

## Goal

Remove per-job Ubuntu package installation from Playwright CI while keeping the
derived image private, immutable at consumption time, and cheap to reuse from
self-hosted GitHub Actions runners.

## Image lifecycle

The repository-owned Dockerfile keeps the tag-and-digest-pinned Microsoft
Playwright image as the base image source of truth, copies the repository-pinned
Bun runtime, installs the repository-pinned Vite+ launcher, adds `tmux`, and removes apt indexes. The derived image is stored privately at
`ghcr.io/kenn-io/middleman-playwright`.

Before any Playwright-dependent job starts, an `ensure_playwright_image` job
computes a tag from the Playwright base digest and the Dockerfile hash. It logs
in to GHCR with the job's `GITHUB_TOKEN` and checks whether that tag exists. If
it exists, the job resolves its immutable manifest digest without rebuilding.
If it does not exist, trusted same-repository runs build and push it, then
resolve the resulting digest. Fork pull requests may consume an existing image
but may not publish a missing one.

New image layers use zstd level 3 with OCI media types. The export format is
part of the recipe key so changing compression publishes one replacement image
instead of silently reusing an older gzip-tagged manifest.

The job exposes the digest-pinned GHCR reference as an output. Full-stack E2E,
mock E2E, and browser-component jobs all depend on that output and authenticate
their private job-container pull with `GITHUB_TOKEN`. They install the checked-out
`bun.lock`, restore Bun's content-addressed package cache without caching
`node_modules`, and invoke the baked Vite+ launcher instead of downloading it
for every job.

## Change detection and failure behavior

The ensure job runs only when backend, frontend, E2E, or CI changes require a
Playwright-backed job. Changing the image recipe is treated as a CI change.

A registry lookup failure other than an absent tag fails the ensure job rather
than silently rebuilding. A missing image on an external fork fails with a
clear message instructing a trusted run to publish it. Image build failures are
isolated to the first run after the base digest or recipe changes; ordinary CI
runs perform only an authenticated manifest check.

## Verification

The workflow is validated with `actionlint`. The existing Playwright version
guard continues to verify that the base image version comment matches every
`@playwright/test` pin. The Dockerfile is built through the same Buildx command
shape used by CI when credentials and registry publication are available.
