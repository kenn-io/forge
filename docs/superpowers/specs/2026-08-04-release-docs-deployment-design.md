# Release-Driven Documentation Deployment

## Status

This design supersedes the Vercel Git deployment design dated 2026-08-03.

## Goal

Publish the documentation for the newest successful kenn-forge release from
the exact source commit that produced its binaries. Keep deployment automatic
without a Vercel Git connection, a GitHub deployment environment, or a human
approval gate.

## Trust Boundary

The tag-triggered `Release` workflow builds binaries and publishes the GitHub
release, but it never receives Vercel credentials. A separate `workflow_run`
workflow exists on protected `main`; GitHub therefore loads the deployment
instructions from the default branch rather than from the release tag.

Its first job has read-only repository access and no Vercel credentials. It
checks out the release run's immutable head SHA, verifies that SHA is reachable
from `main`, resolves GitHub's latest published release tag, and requires that
tag to point to the same SHA. Only a successful validation allows the Vercel
build job to use the repository-level deployment secrets.

## Build and Promotion

Vercel builds the exact validated release SHA as a production-target
deployment with domain promotion disabled. This preserves the production
build environment without allowing build completion order to choose the live
site.

Promotion attempts are not placed in a GitHub concurrency group because GitHub
retains only one pending job and can discard the latest release's only attempt
when builds finish out of order. Immediately before promotion, the workflow
queries GitHub's latest stable release, refetches its tag, peels annotated tags
to a commit, and requires both tag name and commit SHA to equal the validated
values.

A newer release can publish while promotion runs because GitHub release state
and Vercel aliasing have no shared atomic lock. The workflow checks the exact
tag and SHA again immediately after promotion. A stale check before or after
promotion uses its read-only repository access plus `actions: write` to
dispatch the same trusted default-branch workflow. That reconciliation run
resolves the actual latest release itself, then builds and promotes it.
Production is intentionally eventually consistent across the external
check-and-promote boundary, with no lossy queue that can discard the only
reconciliation attempt.

## Failure and Recovery

A failed validation does not expose Vercel credentials. A failed build or
promotion leaves the existing production alias unchanged. Maintainers can
rerun the `Deploy documentation` workflow after a transient failure. If the
automatic path is unavailable, `make docs-deploy` from the latest tagged
checkout remains the operator-controlled recovery path. “Latest” means the
non-draft, non-prerelease release returned by GitHub's latest-release endpoint;
superseded unaliased Vercel deployments may remain in deployment history.

## Verification

`actionlint` validates workflow syntax and expressions. Existing repository
guardrails and documentation checks continue to run unchanged; this repair
does not add workflow contract tests. The Vercel CLI build remains responsible
for compiling the frontend and e2e server, generating the seeded screenshots,
building Zensical, and running the rendered-site checks before a deployment can
be promoted.
