---
title: "Pull Request Workflow"
description: "Public repository privacy, CI runner policy, review preservation, and pull request delivery conventions."
last_edited: "2026-10-02"
---
# Pull Request Workflow

Use this document before pushing, opening a pull request, or changing pull
request metadata, comments, or review threads.

- Treat this as a public repository. Before pushing or opening a pull request,
  run the scrub-private-data skill and remove unnecessary internal project
  names, hostnames, credentials, runner topology, and infrastructure details
  from the diff, commits, and pull request metadata.
- Do not watch or poll pull request GitHub Actions checks unless the user asks,
  or the work is running through the `$kenn:refine-pr` skill.
- Fix known failing CI checks before requesting optional review input or
  screenshot-upload approval. Do not hand off a PR for review while checks fail.
- Runtime image publication belongs to the external release pipeline and only
  published releases may build release images. Do not add repository-owned image
  publication or automatic PR/mainline container builds.
- Public CI profiles use Namespace's [Restricted access level](https://namespace.so/docs/solutions/github-actions/runner-controls/access-levels),
  which disables workload access to Namespace features and APIs. GitHub fork
  approvals, token permissions, and secrets are separate controls.
- Reusable PR CI stays pinned to `main`, with GitHub-hosted fork runners even
  for organization members; workflow validation selects Restricted Namespace
  directly. (`.github/workflows/ci-pr.yml`, `.github/workflows/ci.yml`, `.github/workflows/workflow-validation.yml`)
- Parse Buildx manifest JSON for CI image digests; the plain
  `.Manifest.Digest` formatter can emit a full report
  (`.github/workflows/ci.yml::ensure_playwright_image`).
- Never delete, minimize, hide, or resolve pull request comments, review
  comments, review threads, or CI/review-bot comments unless the user explicitly
  asks for that exact action. Leave stale or contradicted comments in place and
  explain the current evidence in a reply or report.
- Keep pull request descriptions concise. A bulleted summary of user-visible
  changes is sufficient; omit test plans, implementation details, checklists,
  and marketing language.
- For visible UI changes, use the `capture-playwright` skill before opening the
  pull request and attach the screenshot or short video with `gh image` so the
  description can include the resulting artifact links.
