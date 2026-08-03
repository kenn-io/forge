# Public Documentation Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (\`- [ ]\`) syntax for tracking.

**Goal:** Publish a concise, accurate public guide with current generated visuals for onboarding and the main maintainer workflows.

**Approved spec/design:** \`docs/superpowers/specs/2026-08-02-public-documentation-readiness-design.md\`

**Architecture:** Treat the public Markdown files as one documentation product with a short repository landing page, a task-first guide, and separate advanced references. Generate all UI images from isolated seeded servers during the existing staged Zensical build, then inspect the rendered \`site/\` output instead of tracking generated assets.

**Tech Stack:** Markdown, Zensical, TypeScript, Playwright, Node.js, Vite+, Python-based \`unslop\` validation

## Global Constraints

- Apply the \`unslop\` crisp preset to every public Markdown page.
- Preserve commands, paths, provider names, defaults, limits, URLs, and security boundaries exactly unless repository evidence proves they are stale.
- Keep paragraphs short, lead with actions or facts, and describe user outcomes instead of implementation details.
- Keep ADRs, plans, specs, reports, and \`context/\` documents out of the public navigation.
- Generate screenshots only from isolated seeded servers. Never use a live developer app or private data.
- Keep generated SVG files untracked. Commit only their Playwright cases, page references, styles, and build guidance.
- Do not change production onboarding behavior or add product features.
- Keep the pull request based on \`t3code/design-onboarding-mockups\`.

---

### Task 1: Rewrite the public entry path

**Files:**
- Modify: \`README.md\`
- Modify: \`docs/index.md\`
- Modify: \`docs/quickstart.md\`
- Modify: \`docs/zensical.toml\`

**Interfaces:**
- Consumes: the first-run provider flow from PR #816, release artifact names from \`.github/workflows/release.yml\`, and the public page boundaries in the approved spec
- Produces: a short repository landing page, a release-first Quick Start, and task-first site navigation used by every later documentation page

- [ ] **Step 1: Record the original prose constraints and AI-pattern scan**

Run:

\`\`\`bash
for docs_file in README.md docs/index.md docs/quickstart.md; do
  python3 ~/.agents/skills/unslop/scripts/extract_constraints.py "$docs_file"
  python3 ~/.agents/skills/unslop/scripts/banned_phrase_scan.py "$docs_file"
done
\`\`\`

Expected: each command returns JSON. Record any hard or soft violations before editing.

- [ ] **Step 2: Rewrite the repository landing page**

Use \`apply_patch\` to make \`README.md\` contain these sections in order:

1. One short product description and a link to the public guide.
2. A compact list of maintainer outcomes: triage, review, local workspaces, optional Kata and Docs modes, and fleet access.
3. Installation from the GitHub Releases page, including the existing Linux, macOS, and Windows archives.
4. A source-build alternative requiring Go 1.26+ and Bun.
5. The shortest start command and \`http://127.0.0.1:8091\`.
6. Links to Quick Start, Configuration, Workflows, and Troubleshooting.

Remove duplicated configuration tables, provider token routing detail, profiler instructions, and feature walkthroughs that belong in the public guide.

- [ ] **Step 3: Rewrite the docs home and Quick Start**

Use \`apply_patch\` to:

- make \`docs/index.md\` explain the product, identify the first useful tasks, and route readers to Quick Start or a role-based workflow;
- make \`docs/quickstart.md\` lead with release installation, retain source builds as an alternative, start the daemon, and describe the provider-aware onboarding path from code-forge readiness through repository selection, sync, PR selection, and workspace creation;
- keep Settings and TOML as recovery or advanced configuration paths;
- retain optional Kata and Docs mode activation without expanding their setup on this page.

- [ ] **Step 4: Tighten the Zensical navigation**

Use \`apply_patch\` to keep task-first labels and ensure the public home is reachable. Preserve the existing advanced Archive and Fleet pages without exposing internal docs.

- [ ] **Step 5: Validate the rewritten entry path**

Run:

\`\`\`bash
for docs_file in README.md docs/index.md docs/quickstart.md; do
  python3 ~/.agents/skills/unslop/scripts/banned_phrase_scan.py "$docs_file"
  python3 ~/.agents/skills/unslop/scripts/readability_metrics.py "$docs_file"
  python3 ~/.agents/skills/unslop/scripts/diff_check.py <(git show 8d5a5c191:"$docs_file") "$docs_file"
  python3 ~/.agents/skills/unslop/scripts/validate_preservation.py <(git show 8d5a5c191:"$docs_file") "$docs_file"
done
\`\`\`

Expected: zero hard banned-pattern violations. Review every reported missing constraint against the approved information architecture; keep the fact on the most relevant public page when it was intentionally removed from \`README.md\`.

- [ ] **Step 6: Verify and commit the entry path**

Run:

\`\`\`bash
git diff --check
node node_modules/vite-plus/bin/vp exec -- node --test scripts/build-docs.test.mjs scripts/docs-screenshots-command.test.mjs
\`\`\`

Invoke the repository-local `context-sync` skill with `--commit`. Run the
structural `scripts/context-sync --check` first, then perform the semantic
commit review. Include any required context updates and commit the Task 1
files with a conventional `docs:` subject and a rationale-focused body.

---

### Task 2: Rewrite workflow and advanced references

**Files:**
- Modify: \`docs/configuration.md\`
- Modify: \`docs/commands.md\`
- Modify: \`docs/workflows.md\`
- Modify: \`docs/workflows/issue-triager.md\`
- Modify: \`docs/workflows/code-reviewer.md\`
- Modify: \`docs/archive.md\`
- Modify: \`docs/federated-fleet.md\`
- Modify: \`docs/troubleshooting.md\`

**Interfaces:**
- Consumes: the navigation and terminology established in Task 1, current CLI help, config types, provider capabilities, and the approved public page boundaries
- Produces: concise task guides and accurate references linked from the repository landing page and docs home

- [ ] **Step 1: Audit facts against product-owned sources**

Before editing, run:

\`\`\`bash
for docs_file in \
  docs/configuration.md \
  docs/commands.md \
  docs/workflows.md \
  docs/workflows/issue-triager.md \
  docs/workflows/code-reviewer.md \
  docs/archive.md \
  docs/federated-fleet.md \
  docs/troubleshooting.md; do
  python3 ~/.agents/skills/unslop/scripts/extract_constraints.py "$docs_file"
  python3 ~/.agents/skills/unslop/scripts/banned_phrase_scan.py "$docs_file"
done
\`\`\`

Check claims against:

\`\`\`bash
./kenn-forge --help
./kenn-forge serve --help
./kenn-forge start --help
./kenn-forge archive --help
./kenn-forge docs --help
./kenn-forge agent-hook --help
./kenn-forge status --help
\`\`\`

If the binary is absent, build it with \`make build\` before running the help checks. Use \`internal/config/config.go\`, \`.github/workflows/release.yml\`, and the relevant command definitions only to settle claims that the CLI cannot show.

- [ ] **Step 2: Rewrite the routine workflow guides**

Use \`apply_patch\` to make \`docs/workflows.md\` task-first and remove repeated feature inventory. Keep daily triage, navigation, reviews, issues, repository browsing, workspaces, optional modes, and fleet handoff as separate short workflows.

Tighten the two role guides around observable decisions:

- Issue triager: scan recent context, decide the next action, then create a workspace.
- Code reviewer: check recent context and CI, inspect the diff, act on the review, then open a workspace when local verification is needed.

Keep capability differences explicit without repeating the supported-provider list.

- [ ] **Step 3: Rewrite Configuration and Commands as references**

Use \`apply_patch\` to keep exact examples and defaults while shortening explanations. Put common repository, token, mode, agent, docs-folder, and telemetry settings before advanced GitHub App, owner-token, stack, reverse-proxy, and SSE settings. Group commands by the user task they perform and preserve all supported flags shown by CLI help.

- [ ] **Step 4: Rewrite Archive, Fleet, and Troubleshooting around operations**

Use \`apply_patch\` to:

- organize Archive by start, monitor, pause, report, and resume;
- organize Fleet by topology, peer setup, operation, and failure recovery;
- organize Troubleshooting by symptom, direct check, and recovery command;
- remove internal rationale and repeated architecture detail unless it changes safe operation.

- [ ] **Step 5: Validate facts and prose across the full public corpus**

Run:

\`\`\`bash
for docs_file in \
  docs/configuration.md \
  docs/commands.md \
  docs/workflows.md \
  docs/workflows/issue-triager.md \
  docs/workflows/code-reviewer.md \
  docs/archive.md \
  docs/federated-fleet.md \
  docs/troubleshooting.md; do
  python3 ~/.agents/skills/unslop/scripts/banned_phrase_scan.py "$docs_file"
  python3 ~/.agents/skills/unslop/scripts/readability_metrics.py "$docs_file"
  python3 ~/.agents/skills/unslop/scripts/diff_check.py <(git show 8d5a5c191:"$docs_file") "$docs_file"
  python3 ~/.agents/skills/unslop/scripts/validate_preservation.py <(git show 8d5a5c191:"$docs_file") "$docs_file"
done
\`\`\`

Review intentional fact movement across the complete public corpus. No command, default, limit, provider boundary, or security constraint may disappear from all public pages.

Expected: zero hard banned-pattern violations and no unexplained missing constraints.

- [ ] **Step 6: Verify and commit the reference rewrite**

Run:

\`\`\`bash
git diff --check
node node_modules/vite-plus/bin/vp exec -- node --test scripts/build-docs.test.mjs scripts/docs-screenshots-command.test.mjs
\`\`\`

Invoke the repository-local `context-sync` skill with `--commit`. Include any
required context updates, then commit the Task 2 files with a conventional
`docs:` subject and a rationale-focused body.

---

### Task 3: Refresh and expand generated documentation visuals

**Files:**
- Modify: \`docs/screenshots/docs-screenshots.spec.ts\`
- Modify: \`docs/screenshots/README.md\`
- Modify: \`docs/stylesheets/extra.css\`
- Modify: \`docs/index.md\`
- Modify: \`docs/quickstart.md\`
- Modify: \`docs/workflows/issue-triager.md\`
- Modify: \`docs/workflows/code-reviewer.md\`

**Interfaces:**
- Consumes: \`startIsolatedWorkspaceE2EServer()\`, \`startIsolatedE2EServerWithOptions()\`, the seeded \`acme/widgets\` fixtures, onboarding storage keys, and \`KENN_FORGE_DOCS_SCREENSHOT_DIR\`
- Produces: generated \`first-run-{light,dark}.svg\`, \`maintainer-overview-{light,dark}.svg\`, \`issue-triager-{light,dark}.svg\`, and \`code-reviewer-{light,dark}.svg\` in the staged docs asset directory

- [ ] **Step 1: Apply the repository test-scope guidance**

Read \`kenn-test-scope-discipline:test-scope-discipline\` before changing the Playwright generator. Keep assertions focused on stable visible state, theme selection, settled loading, and privacy-safe fixture data.

- [ ] **Step 2: Generalize the capture cases without changing production code**

Use \`apply_patch\` to extend \`CaptureCase.name\` with \`maintainer-overview\` and add light and dark overview cases at \`/\`. Use \`.activity-feed\` as the ready selector and a seeded item title as the ready text. Keep issue and code-review cases on their current provider-aware routes.

Extract one capture helper that:

- prepares the selected theme;
- opens the case route;
- disables transitions and animation;
- waits for the case's stable visible selector and text;
- waits for sync and loading indicators to settle when that case has sync controls;
- serializes the current DOM into the existing accessible SVG wrapper;
- writes the file under \`KENN_FORGE_DOCS_SCREENSHOT_DIR\`.

- [ ] **Step 3: Add an isolated first-run capture**

Start a second server with \`startIsolatedE2EServerWithOptions()\`. Remove its configured repositories through the public settings API, clear \`kenn-forge:first-run-onboarding\` in local and session storage, and route \`/api/v1/tooling-status\` to the synthetic authenticated \`github.com\` maintainer used by the onboarding e2e test.

Capture light and dark \`first-run\` SVGs only after the \`Connect a code forge\` heading and provider readiness list are visible. Stop this server in teardown. Do not reuse or mutate the configured workflow server.

- [ ] **Step 4: Place each visual next to the task it explains**

Use \`apply_patch\` to add theme-aware \`<figure>\` blocks:

- first-run on \`docs/quickstart.md\`;
- maintainer overview on \`docs/index.md\`;
- refreshed issue triage and code review on their existing role guides.

Write specific alt text and one-sentence captions. Extend \`docs/stylesheets/extra.css\` only if the new placements reveal a real rendering issue. Update \`docs/screenshots/README.md\` with all eight generated names and the isolated-data rule.

- [ ] **Step 5: Run the screenshot and build tests**

Run:

\`\`\`bash
node node_modules/vite-plus/bin/vp exec -- node --test scripts/build-docs.test.mjs scripts/docs-screenshots-command.test.mjs
mkdir -p tmp/docs-screenshots
env KENN_FORGE_DOCS_SCREENSHOT_DIR=tmp/docs-screenshots node node_modules/vite-plus/bin/vp exec -- playwright test --config docs/screenshots/playwright.config.ts --project=chromium
node scripts/build-docs.mjs
\`\`\`

Expected: all screenshot cases pass, all eight SVGs appear in staged build output, and Zensical writes the rendered site to \`site/\`.

- [ ] **Step 6: Inspect generated artifacts and rendered pages**

Use the local image viewer for each generated light and dark SVG. Check current UI, stable synthetic data, clipping, loading states, and private information. Serve \`site/\` locally, then use the T3 preview browser to inspect the docs home, Quick Start, both role guides, navigation, links, theme switching, and a 390px-wide viewport.

Confirm raw source paths that look suspicious against the rendered \`site/\` output. Confirm \`git status --short\` does not list \`docs/assets/generated/\` or generated SVGs.

- [ ] **Step 7: Run final prose and repository verification**

Run banned-pattern, readability, and preservation checks over every public Markdown page. Then run:

\`\`\`bash
git diff --check
node node_modules/vite-plus/bin/vp exec -- node --test scripts/*.test.mjs scripts/*.test.ts
git status --short
\`\`\`

Use \`superpowers:verification-before-completion\` before reporting success.

- [ ] **Step 8: Commit the visuals and final corrections**

Invoke the repository-local `context-sync` skill with `--commit`. Include any
required context updates, then commit the screenshot generator, screenshot
guidance, styles, and final page corrections with a conventional `docs:`
subject and a rationale-focused body.

---

### Task 4: Publish the stacked pull request

**Files:**
- No repository file changes expected

**Interfaces:**
- Consumes: the verified commits from Tasks 1 through 3 and onboarding PR #816
- Produces: a pushed branch and a pull request whose base is \`t3code/design-onboarding-mockups\`

- [ ] **Step 1: Scrub public metadata and the complete diff**

The repository references a \`scrub-private-data\` skill that is not installed in this session. Perform the fallback manually with \`git diff origin/t3code/design-onboarding-mockups...HEAD\`, \`git log\`, and targeted searches for home paths, private hosts, credentials, runner topology, and private project names. Generic seeded names such as \`acme/widgets\` are allowed.

- [ ] **Step 2: Push and link the stack**

Push \`t3code/refine-onboarding-documentation\` to \`origin\`. Use non-interactive \`gh stack link --base main --open 816 t3code/refine-onboarding-documentation\` so the new PR is based on \`t3code/design-onboarding-mockups\` and linked above PR #816.

- [ ] **Step 3: Set concise public PR metadata**

Use \`gh pr edit\` to set a concise title and a body containing only a bulleted summary of user-visible documentation changes. Do not include a test plan, checklist, implementation detail, or marketing language. End any GitHub-authored text with:

\`\`\`html
<sup>generated by a clanker</sup>
\`\`\`

- [ ] **Step 4: Verify the remote stack**

Run \`gh stack view --json\` and \`gh pr view\` for the new PR. Confirm the new PR is open, its base is \`t3code/design-onboarding-mockups\`, and PR #816 remains the lower stack layer. Do not poll CI.
