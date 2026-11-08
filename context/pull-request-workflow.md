# Pull Request Workflow

Use this document before pushing, opening a pull request, or changing pull
request metadata, comments, or review threads.

- Treat this as a public repository. Before pushing or opening a pull request, run the
  scrub-private-data skill and remove unnecessary internal project names, hostnames, credentials,
  runner topology, and infrastructure details from the diff, commits, and pull request metadata.
- Do not watch or poll pull request GitHub Actions checks unless the user asks,
  or the work is running through the `$kenn:refine-pr` skill.
- Fix known failing CI checks before requesting optional review input or
  screenshot-upload approval. Do not hand off a PR for review while checks fail.
- Before each push to an open PR, read its newest `roborev-ci` comment
  (`gh api repos/<owner>/<repo>/issues/<n>/comments`) and fix or answer each finding;
  local roborev reviews one commit at a time and does not replace it.
- Judge CI by the runs for the PR's current head. Cancelled runs from superseded pushes
  and runs on pre-rebase SHAs are noise.
- Runtime image publication belongs to the external release pipeline and only
  published releases may build release images. Do not add repository-owned image
  publication or automatic PR/mainline container builds.
- CI jobs use GitHub-hosted runners, including pull requests from forks,
  workflow validation, and scheduled Android builds. (`.github/workflows/`)
- The `CI gate`, `Detect changed paths`, and `Sibling pins on main` jobs take
  seconds. They run on the Namespace profile `namespace-profile-linux-1cpu` so
  they do not wait in the GitHub-hosted queue. (`.github/workflows/ci-pr.yml::jobs.gate`)
- macOS 15 (Sequoia) is the minimum supported macOS version. Keep macOS CI on
  that baseline; runner upgrades require a support-policy change.
  (`.github/workflows/ci.yml::jobs.daemon-path-macos`)
- Workflow `uses:` lines name version tags; `.github/workflows/actions.lock` pins every action,
  including transitive ones, to satisfy the org's SHA-pinning policy. Run `make actions-lock` after
  changing any `uses:` line; a stale lockfile fails jobs at setup (`Makefile::actions-lock`).
- PR CI calls the reusable workflow from the same commit so checks exercise
  the proposed workflow changes. (`.github/workflows/ci-pr.yml::jobs.run`)
- When changing test filters, confirm the step ran tests; a filter matching
  nothing can still pass.
- Parse Buildx manifest JSON for CI image digests; the plain
  `.Manifest.Digest` formatter can emit a full report
  (`.github/workflows/ci.yml::ensure_playwright_image`).
- Never delete, minimize, hide, or resolve pull request comments, review comments, review threads, or
  CI/review-bot comments unless the user explicitly asks for that exact action. Leave stale or
  contradicted comments in place and explain the current evidence in a reply or report.
- Keep pull request descriptions concise. A bulleted summary of user-visible
  changes is sufficient; omit test plans, implementation details, checklists,
  and marketing language.
- For visible UI changes, use the `capture-playwright` skill and attach the
  screenshot or short video with `--attach`: on `gh pr create` when opening the
  pull request, or on `gh pr edit` or `gh pr comment` for an existing one.

## Stacked pull requests

- Rebasing every branch of a stack onto main is routine; do not stop a stack rebase
  because it moves the bottom branch (maintainer decision).
- `gh stack rebase` finds fork points through the reflog, so after a branch in the stack
  was reset it can silently drop commits from the branches above. Compare
  `git log --oneline <parent>..<branch>` before and after every stack rebase. Save each
  branch tip and parent tip first. After a lossy rebase, restore the branch to its saved
  tip, then redo it with `git rebase --onto <new-parent> <old-parent-tip> <branch>`.
