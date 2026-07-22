# Private Playwright CI Image Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace repeated Ubuntu and frontend-tool bootstrap downloads with a private GHCR Playwright image that CI creates only when its pinned toolchain or recipe changes.

**Architecture:** A small Dockerfile adds the pinned Bun runtime, Vite+ launcher, and `tmux` to the pinned Microsoft Playwright image. A prerequisite GitHub Actions job checks a content-derived GHCR tag, publishes it only when missing on a trusted run, resolves its manifest digest, and passes that immutable reference to every Playwright job.

**Tech Stack:** GitHub Actions, Docker Buildx, GHCR, Playwright, Ubuntu Noble

## Global Constraints

- Keep the GHCR package private.
- Authenticate all GHCR pulls with the workflow `GITHUB_TOKEN`.
- Never expose package-write permission to an external fork pull request.
- Keep the Playwright base image pinned by SHA-256 digest and version-matched to `@playwright/test`.
- Do not run apt installation in each Playwright test job.

---

### Task 1: Add the derived image recipe

**Files:**

- Create: `.github/docker/playwright/Dockerfile`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**

- Consumes: the tag-and-digest-pinned `PLAYWRIGHT_BASE_IMAGE` default, which CI may pass explicitly as a build argument.
- Produces: a Linux amd64 image containing the Playwright browsers, their OS dependencies, Bun, Vite+, and `tmux`.

- [x] **Step 1: Add the Dockerfile**

Create a Dockerfile whose `FROM` uses a tag-and-digest-pinned
`PLAYWRIGHT_BASE_IMAGE`, replaces the Ubuntu archive endpoints with an HTTPS
mirror, installs `tmux` with bounded apt retries and timeouts, and deletes
`/var/lib/apt/lists/*`.

- [x] **Step 2: Include the recipe in CI change detection**

Add `.github/docker/**` to the `ci` path filter so recipe changes exercise the
image resolver and affected Playwright jobs.

### Task 2: Ensure and resolve the private image

**Files:**

- Modify: `.github/workflows/ci.yml`

**Interfaces:**

- Consumes: `detect_changes` outputs, the pinned `PLAYWRIGHT_BASE_IMAGE`, and `.github/docker/playwright/Dockerfile`.
- Produces: job output `image`, formatted as `ghcr.io/kenn-io/middleman-playwright@sha256:<manifest-digest>`.

- [x] **Step 1: Add the prerequisite job**

Add `ensure_playwright_image` after `detect_changes`. Give it `contents: read`
and `packages: write`, but gate publication so fork pull requests cannot push.

- [x] **Step 2: Compute and inspect the content tag**

Read the base image from the Dockerfile, extract its SHA-256 digest, hash the
Dockerfile, and form a tag containing both values. Authenticate to GHCR with `GITHUB_TOKEN` and use
`docker buildx imagetools inspect` to determine whether it exists.

- [x] **Step 3: Publish only when absent**

When the tag does not exist on a trusted run, invoke `docker buildx build` with
`--platform linux/amd64`, the pinned base build argument, `--push`, and
`--provenance=false`. Do not rebuild an existing tag.

- [x] **Step 4: Resolve the immutable digest**

Inspect the tag after the optional build and append the digest-pinned reference
to `$GITHUB_OUTPUT` as `image=<reference>`.

### Task 3: Consume the private image

**Files:**

- Modify: `.github/workflows/ci.yml`

**Interfaces:**

- Consumes: `needs.ensure_playwright_image.outputs.image`.
- Produces: authenticated private job-container pulls for `e2e-mock`, `e2e`, and `test_browser`.

- [x] **Step 1: Add the image dependency and read permission**

Make each Playwright job depend on both `detect_changes` and
`ensure_playwright_image`, and give each job `contents: read` and
`packages: read`.

- [x] **Step 2: Replace static MCR job containers**

Set each `container.image` to the ensure job output and add container
credentials using `${{ github.actor }}` and `${{ github.token }}`.

- [x] **Step 3: Remove runtime apt installation**

Delete the `Install tmux for workspace PTY sessions` step from the full-stack
E2E job because `tmux` is now part of the job image.

- [x] **Step 4: Use the baked frontend bootstrap**

Restore Bun's content-addressed package cache, install the checked-out `bun.lock`
with the baked Bun runtime, and run the baked, package-pinned Vite+ launcher
instead of downloading it in each Playwright job.

### Task 4: Verify the workflow

**Files:**

- Verify: `.github/workflows/ci.yml`
- Verify: `.github/docker/playwright/Dockerfile`

**Interfaces:**

- Consumes: the completed workflow and image recipe.
- Produces: syntax and guardrail evidence suitable for committing.

- [x] **Step 1: Validate GitHub Actions syntax**

Run `actionlint .github/workflows/ci.yml`; expect exit code 0.

- [x] **Step 2: Run the Playwright version guard**

Run `make playwright-version-check`; expect exit code 0 and no findings.

- [x] **Step 3: Run repository script checks**

Run `make script-tests`; expect all script tests to pass.

- [x] **Step 4: Review the diff and commit**

Run the repository `context-sync --commit` workflow, review `git diff`, then
create a conventional commit explaining that the private pre-baked image
removes Ubuntu mirror downloads from Playwright jobs.
