# Release-Driven Documentation Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy documentation automatically from the newest trusted kenn-forge release without a GitHub deployment environment.

**Approved spec/design:** `docs/superpowers/specs/2026-08-04-release-docs-deployment-design.md`

**Architecture:** Keep binary publication in the tag-triggered `Release` workflow and move documentation deployment to a default-branch `workflow_run` workflow. Validate release provenance without Vercel credentials, build without aliasing, and automatically reconcile a promotion that becomes stale.

**Tech Stack:** GitHub Actions, GitHub CLI, Git, Bun, Vercel CLI

## Global Constraints

- Do not use a GitHub deployment environment or human approval gate.
- Do not expose Vercel credentials to the tag-loaded release workflow.
- Admit a build only while its release is latest; superseded unaliased builds
  may finish, but only the latest validated tag and SHA may be promoted.
- Do not add Windows architecture tests as part of this review repair.
- Do not add workflow contract tests as part of this review repair.
- Preserve landed dated plans and specifications.

---

### Task 1: Move deployment behind the default-branch trust boundary

**Files:**
- Create: `.github/workflows/deploy-docs.yml`
- Modify: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: the completed `Release` workflow run's `head_sha`.
- Produces: `validate-release` outputs `release-tag` and `release-sha`; `build` outputs `deployment-url`; `promote` assigns the production alias or dispatches latest-release reconciliation.

- [x] **Step 1: Remove Vercel deployment from the tag-loaded workflow**

Delete `deploy-docs` from `.github/workflows/release.yml` so the tag-selected
workflow never references Vercel credentials.

- [x] **Step 2: Validate release provenance without Vercel credentials**

Create `.github/workflows/deploy-docs.yml` with `permissions: contents: read`
and a `workflow_run` trigger for a completed `Release`. Require
`github.event.workflow_run.conclusion == 'success'`. Check out
`github.event.workflow_run.head_sha` with `fetch-depth: 0`, explicitly fetch
`origin/main` and the latest release tag, require the SHA to be an ancestor of
`origin/main`, and require the tag to resolve to the same SHA.

- [x] **Step 3: Separate production building from promotion**

On the fresh build runner, check out
`needs.validate-release.outputs.release-sha`, run
`vercel deploy --prod --skip-domain`, capture the deployment URL, then pass it
to the `promote` job. Do not use GitHub concurrency for release publication or
promotion, because it may discard the only pending attempt when builds finish
out of order.

- [x] **Step 4: Recheck release freshness before promotion**

Set up the pinned Bun runtime before the freshness check. Then query
`repos/$GITHUB_REPOSITORY/releases/latest` without Vercel credentials, using
only the read-only GitHub token. Refetch and peel the tag, require its name and
commit to equal `needs.validate-release.outputs.release-tag` and
`needs.validate-release.outputs.release-sha`, and run `vercel promote` in the
immediately following step. Query and peel the latest tag again immediately
after promotion. Give only the promotion job `actions: write`; when either
freshness check is stale, dispatch `deploy-docs.yml` on `main` so a non-lossy
trusted run resolves and deploys the actual latest release.

- [x] **Step 5: Validate workflow syntax**

Run: `actionlint .github/workflows/release.yml .github/workflows/deploy-docs.yml`

Expected: both workflows pass.

---

### Task 2: Preserve the decision history and document recovery

**Files:**
- Modify: `context/docs-authoring.md`
- Modify: `docs/README.md`
- Restore: `docs/superpowers/plans/2026-07-30-kenn-forge-rename.md`
- Restore: `docs/superpowers/plans/2026-07-31-legacy-config-migration.md`
- Restore: `docs/superpowers/plans/2026-08-01-onboarding-prototypes.md`
- Restore: `docs/superpowers/plans/2026-08-02-public-documentation-readiness.md`
- Restore: `docs/superpowers/plans/2026-08-03-generated-workflow-screenshot-lightbox.md`
- Restore: `docs/superpowers/specs/2026-07-30-kenn-forge-rename-design.md`
- Restore: `docs/superpowers/specs/2026-07-31-legacy-config-migration-design.md`
- Restore: `docs/superpowers/specs/2026-08-01-onboarding-prototypes-design.md`
- Restore: `docs/superpowers/specs/2026-08-02-provider-readiness-onboarding-design.md`
- Restore: `docs/superpowers/specs/2026-08-02-public-documentation-readiness-design.md`
- Restore: `docs/superpowers/specs/2026-08-03-generated-workflow-screenshot-lightbox-design.md`
- Restore: `docs/superpowers/specs/2026-08-03-huma-transport-inventory-design.md`
- Restore: `docs/superpowers/specs/2026-08-03-vercel-git-docs-deployment-design.md`

**Interfaces:**
- Consumes: the workflow behavior from Task 1.
- Produces: current maintainer guidance and an unchanged historical design record.

- [x] **Step 1: Restore historical documents**

Restore landed plans/specifications and the original 2026-08-03 Vercel Git
design instead of rewriting their recorded decisions.

- [x] **Step 2: Update living deployment guidance**

Document the default-branch trust check, non-aliasing Vercel build, serialized
latest-release promotion, lack of an environment gate, workflow rerun, and
manual tagged-checkout recovery path.

- [x] **Step 3: Run repository workflow and documentation checks**

Run: `actionlint .github/workflows/release.yml .github/workflows/deploy-docs.yml`

Expected: workflow validation passes.

Run: `make guardrail-check`

Expected: repository guardrails pass.

Run: `node --test scripts/check-docs-branding.test.mjs && node scripts/check-docs-branding.mjs`

Expected: the branding test and guard pass.

Run: `git diff --check`

Expected: no whitespace errors.
