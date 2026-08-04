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

A separate promotion job uses the shared `docs-production` concurrency group.
Immediately before promotion it queries GitHub's latest release again and
requires the tag to equal the one validated for this run. A superseded run may
finish building, but it cannot claim `forge.kenn.io` after a newer release is
published.

## Failure and Recovery

A failed validation does not expose Vercel credentials. A failed build or
promotion leaves the existing production alias unchanged. Maintainers can
rerun the `Deploy documentation` workflow after a transient failure. If the
automatic path is unavailable, `make docs-deploy` from the latest tagged
checkout remains the recovery path.

## Verification

`actionlint` validates workflow syntax and expressions. Existing repository
guardrails and documentation checks continue to run unchanged; this repair
does not add workflow contract tests. The Vercel CLI build remains responsible
for compiling the frontend and e2e server, generating the seeded screenshots,
building Zensical, and running the rendered-site checks before a deployment can
be promoted.
