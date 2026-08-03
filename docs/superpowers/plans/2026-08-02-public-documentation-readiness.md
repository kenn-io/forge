# Public Documentation Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (\`- [ ]\`) syntax for tracking.

**Goal:** Publish a concise, accurate public guide with current generated visuals for onboarding and the main maintainer workflows.

**Approved spec/design:** \`docs/superpowers/specs/2026-08-02-public-documentation-readiness-design.md\`

**Architecture:** Treat the public Markdown files as one documentation product with a short repository landing page, a task-first guide, and separate advanced references. Generate all UI images from isolated seeded servers during the existing staged Zensical build, then inspect the rendered \`site/\` output instead of tracking generated assets.

**Tech Stack:** Markdown, Zensical, TypeScript, Playwright, Node.js, Vite+, Python-based \`unslop\` validation

**Execution status:** Tasks 1 through 3 are complete in commits through
`7d0a3cd87`. Resume at Task 4; do not repeat the earlier rewrites or eight
existing captures.

## Global Constraints

- Apply the \`unslop\` crisp preset to every public Markdown page.
- Preserve commands, paths, provider names, defaults, limits, URLs, and security boundaries exactly unless repository evidence proves they are stale.
- Keep paragraphs short, lead with actions or facts, and describe user outcomes instead of implementation details.
- Keep ADRs, plans, specs, reports, \`context/\`, and build tooling out of
  both the staged Zensical input and rendered public site.
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

### Task 4: Close public-site and reviewer-workspace gaps

**Files:**
- Modify: `scripts/build-docs.mjs`
- Modify: `scripts/build-docs.test.mjs`
- Modify: `docs/zensical.toml`
- Modify: `docs/stylesheets/extra.css`
- Modify: `README.md`
- Modify: `docs/quickstart.md`
- Modify: `docs/workflows/code-reviewer.md`
- Modify: `docs/screenshots/docs-screenshots.spec.ts`
- Modify: `docs/screenshots/README.md`
- Create: `docs/site/docs-site.spec.ts`
- Create: `docs/site/playwright.config.ts`

**Interfaces:**
- Consumes: `stageDocsSource(sourceDir, destinationDir)`,
  `startIsolatedWorkspaceE2EServer()`, the seeded `acme/widgets#1` pull
  request, and the existing `captureCase()` SVG serializer
- Produces: a public-only staged docs tree and four additional generated SVGs:
  `code-reviewer-agent-launch-{light,dark}.svg` and
  `workspace-codex-session-{light,dark}.svg`

- [ ] **Step 1: Prove internal docs leak through staging**

Extend `scripts/build-docs.test.mjs` so its fixture contains public inputs and
internal inputs:

```js
await mkdir(path.join(source, "stylesheets"), { recursive: true });
await mkdir(path.join(source, "workflows"), { recursive: true });
await mkdir(path.join(source, "superpowers", "plans"), { recursive: true });
await mkdir(path.join(source, "adr"), { recursive: true });
await mkdir(path.join(source, "reports"), { recursive: true });
await mkdir(path.join(source, "screenshots"), { recursive: true });
await writeFile(path.join(source, "index.md"), "# Docs\n");
await writeFile(path.join(source, "workflows", "code-reviewer.md"), "# Reviewer\n");
await writeFile(path.join(source, "stylesheets", "extra.css"), ":root {}\n");
await writeFile(path.join(source, "superpowers", "plans", "private.md"), "# Private\n");
await writeFile(path.join(source, "adr", "0001-private.md"), "# Private\n");
await writeFile(path.join(source, "reports", "private.md"), "# Private\n");
await writeFile(path.join(source, "screenshots", "README.md"), "# Build only\n");
await writeFile(path.join(source, "workflows", "internal.md"), "# Private\n");
await writeFile(path.join(source, "stylesheets", "internal.css"), ":root {}\n");
```

After `stageDocsSource()`, assert that `index.md`, the workflow page, and the
stylesheet exist. Assert `ENOENT` for every internal fixture path and for a
stale `assets/generated/stale.svg`. The rejected fixtures must include
`workflows/internal.md` and `stylesheets/internal.css`, proving that allowed
directories are traversal paths rather than recursive publication roots.

- [ ] **Step 2: Run the staging test and verify the leak is detected**

Run:

```bash
node node_modules/vite-plus/bin/vp exec -- node --test scripts/build-docs.test.mjs
```

Expected: FAIL because the current recursive copy stages
`superpowers/plans/private.md`.

- [ ] **Step 3: Stage only explicit public inputs**

In `scripts/build-docs.mjs`, replace the generated-asset-only filter with:

```js
const publishedFiles = new Set([
  "archive.md",
  "commands.md",
  "configuration.md",
  "federated-fleet.md",
  "index.md",
  "quickstart.md",
  "stylesheets/extra.css",
  "troubleshooting.md",
  "workflows/code-reviewer.md",
  "workflows/issue-triager.md",
  "workflows.md",
]);
const publishedDirectoryEntries = new Set(["stylesheets", "workflows"]);

function isPublishedDocsInput(sourceDir, candidate) {
  const relative = path.relative(sourceDir, candidate);
  if (relative === "") return true;
  return publishedFiles.has(relative) || publishedDirectoryEntries.has(relative);
}
```

Use `isPublishedDocsInput` as the `cp` filter. Continue copying
`zensical.toml` separately and generating `assets/generated/` only in the
staged tree. The two directory entries allow `cp` to traverse to explicitly
listed files; they do not publish unlisted descendants.

- [ ] **Step 4: Verify the public staging boundary**

Run the test from Step 2 again.

Expected: PASS with public pages and styles preserved and every internal input
absent.

- [ ] **Step 5: Add a rendered-site header regression test**

Create `docs/site/playwright.config.ts` with one Chromium project and a local
static server:

```ts
import { defineConfig, devices } from "@playwright/test";

const siteDir = process.env.KENN_FORGE_DOCS_SITE_DIR;
if (!siteDir) throw new Error("KENN_FORGE_DOCS_SITE_DIR must point to rendered site output");

export default defineConfig({
  testDir: ".",
  testMatch: "docs-site.spec.ts",
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:4178",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 } },
    },
  ],
  webServer: {
    command: "python3 -m http.server 4178 --bind 127.0.0.1",
    cwd: siteDir,
    url: "http://127.0.0.1:4178",
    reuseExistingServer: false,
  },
});
```

Create `docs/site/docs-site.spec.ts` with a test that opens `/`, records the
visible first `.md-header__topic` text, `fontFamily`, `fontWeight`, and
`transform`, scrolls to 500px, waits for the 400ms Zensical transition, and
asserts the following at both `{ width: 1280, height: 800 }` and
`{ width: 390, height: 844 }`:

```ts
await expect(brand).toHaveText("kenn-forge");
await expect(brand).toBeVisible();
await expect(pageTitle).toBeHidden();
expect(after.fontFamily).toBe(before.fontFamily);
expect(after.fontWeight).toBe(before.fontWeight);
expect(after.transform).toBe("none");
```

Run against the current rendered `site/`:

```bash
env KENN_FORGE_DOCS_SITE_DIR=site \
  node node_modules/vite-plus/bin/vp exec -- playwright test \
  --config docs/site/playwright.config.ts --project=chromium
```

Expected: FAIL because Zensical hides `kenn-forge` and shows the regular-weight
`Kenn Forge` page topic after scrolling.

- [ ] **Step 6: Keep header branding stable**

Append this minimal override to `docs/stylesheets/extra.css`:

```css
.md-header__topic:first-child {
  opacity: 1;
  pointer-events: auto;
  transform: none;
  z-index: 0;
}

.md-header__topic + .md-header__topic {
  display: none;
}
```

Rebuild with `node scripts/build-docs.mjs`, then rerun the rendered-site test
from Step 5 at 1280px and at 390px. Both viewports must keep the same visible,
unclipped `kenn-forge` brand.

After `uvx zensical build` in `scripts/build-docs.mjs`, invoke this
rendered-site Playwright project with
`KENN_FORGE_DOCS_SITE_DIR=path.join(stagingRoot, "site")`. The docs build must
fail if the rendered-site test fails.

- [ ] **Step 7: Correct public repository and release links**

Add a second test to `docs/site/docs-site.spec.ts` that requires the rendered
home repository link to target `https://github.com/kenn-io/forge` and the
Quick Start `GitHub Releases` link to target
`https://github.com/kenn-io/forge/releases`. Run the rendered-site project.

Expected before the URL edit: FAIL because both links still target
`kenn-io/middleman`.

Record the original constraints and banned-pattern result before editing:

```bash
for docs_file in README.md docs/quickstart.md; do
  python3 /Users/mariusvniekerk/.agents/skills/unslop/scripts/extract_constraints.py "$docs_file"
  python3 /Users/mariusvniekerk/.agents/skills/unslop/scripts/banned_phrase_scan.py "$docs_file"
done
```

Replace public `kenn-io/middleman` URLs with `kenn-io/forge` in
`README.md`, `docs/quickstart.md`, and `docs/zensical.toml`. This includes
release URLs, source clone commands, `repo_url`, and `repo_name`. Do not
rewrite historical internal plans or specs.

Run:

```bash
if rg -n 'github\.com/kenn-io/middleman|repo_name = "kenn-io/middleman"' \
  README.md docs/index.md docs/quickstart.md docs/configuration.md \
  docs/commands.md docs/workflows.md docs/workflows docs/archive.md \
  docs/federated-fleet.md docs/troubleshooting.md docs/zensical.toml; then
  exit 1
fi
```

Expected: no matches and exit 0. Rebuild the site and rerun the rendered-site
project. Both canonical-link assertions must pass.

- [ ] **Step 8: Add synthetic Codex workspace preparation**

In `docs/screenshots/docs-screenshots.spec.ts`, extend `CaptureCase.name`
with `code-reviewer-agent-launch` and `workspace-codex-session`. Add optional
`prepare(page, baseURL)` and `afterReady(page)` callbacks. Call `prepare`
before navigation and `afterReady` after stable selectors and loading checks,
but before SVG serialization.

Add `configureSyntheticCodexAgent(page, baseURL)` that writes this agent
through `PUT /api/v1/settings`:

```ts
{
  agents: [
    {
      key: "codex",
      label: "Codex",
      command: ["/bin/sh", "-lc", "while :; do sleep 3600; done"],
      enabled: true,
    },
  ],
}
```

Assert the update returns status 200 and its `launch_targets` array contains
an available target with `key: "codex"` before continuing.

Add `ensureSyntheticCodexWorkspace(page, baseURL)` that:

1. Configures the synthetic agent.
2. Reuses the `acme/widgets#1` workspace when present, or posts
   `{ provider: "github", platform_host: "github.com", owner: "acme",
   name: "widgets", mr_number: 1 }` to `POST /api/v1/workspaces`.
3. Polls `GET /api/v1/workspaces/{id}` until status is `ready`, failing on
   `error`.
4. Reads `GET /api/v1/workspaces/{id}/runtime` and posts
   `{ target_key: "codex" }` to
   `POST /api/v1/workspaces/{id}/runtime/sessions` only when needed.
5. Returns the workspace ID.

The command is a synthetic long-running shell. It must never invoke the
installed `codex` binary or read developer configuration or credentials.

- [ ] **Step 9: Add the reviewer-to-Codex captures**

Add light and dark `code-reviewer-agent-launch` cases at
`/pulls/github/acme/widgets/1`. Their `prepare` callback configures the
synthetic agent. Their `afterReady` callback clicks the accessible
`Create Workspace options` button and waits for the `Codex` menu item.

Add light and dark `workspace-codex-session` cases at `/workspaces`. Their
`prepare` callback calls `ensureSyntheticCodexWorkspace`. Their
`readySelector` is `.workspace-list-sidebar` with ready text
`Add widget caching layer`. Their `afterReady` callback selects that row and
waits for both `.workspace-list-sidebar .ws-row.selected` and the `Codex` tab
inside the `Workflow panes` region.

Update `docs/screenshots/README.md` with the four filenames and the rule that
the Codex process is synthetic.

- [ ] **Step 10: Expand the code-reviewer workflow**

Before editing, run constraint extraction and the banned-pattern scan on
`docs/workflows/code-reviewer.md`.

In `docs/workflows/code-reviewer.md`, replace the short local-verification
paragraph with:

```markdown
## Move from review into a coding agent

Open **Create Workspace** and choose a configured agent such as Codex. Kenn
Forge automatically creates and tracks a Git worktree for the pull-request
branch, then launches the agent in that worktree. You do not need to run
`git worktree add` or manage a separate checkout.
```

Place the `code-reviewer-agent-launch` light/dark figure after that paragraph.
Follow it with a short paragraph explaining that Workspaces keeps the branch,
session, and review context together, then place the
`workspace-codex-session` light/dark figure. Use specific alt text and
one-sentence captions.

- [ ] **Step 11: Verify all captures and rendered output**

Run:

```bash
env KENN_FORGE_DOCS_SCREENSHOT_DIR=tmp/docs-screenshots \
  node node_modules/vite-plus/bin/vp exec -- playwright test \
  --config docs/screenshots/playwright.config.ts --project=chromium
node scripts/build-docs.mjs
```

Expected: 12 screenshot cases pass and Zensical builds the public site. Inspect
all four new SVGs in light and dark mode. Confirm the menu shows Codex, the
Workspaces capture shows the selected PR worktree and Codex session, no terminal
content or private data appears, and every app icon renders standalone.

Serve `site/` and inspect `/workflows/code-reviewer/` at 1280px and 390px.
Verify both new figures load, their alt text and captions match the workflow,
theme switching selects the correct light/dark assets, navigation contains only
public pages, and scrolling keeps `kenn-forge` unchanged. Inspect the rendered
home and Quick Start links and confirm their resolved `href` values target
`kenn-io/forge`.

Confirm these paths do not exist:

```bash
test ! -e site/superpowers
test ! -e site/adr
test ! -e site/reports
test ! -e site/screenshots
```

Also request `/superpowers/`, `/adr/`, `/reports/`, and `/screenshots/`
from the served site and confirm each returns 404.

- [ ] **Step 12: Run final prose and repository verification**

Run the `unslop` banned-pattern and readability checks over all 11 public
Markdown pages. For `README.md`, `docs/quickstart.md`, and
`docs/workflows/code-reviewer.md`, compare the final file with its Task 4
baseline:

```bash
for docs_file in README.md docs/quickstart.md docs/workflows/code-reviewer.md; do
  python3 /Users/mariusvniekerk/.agents/skills/unslop/scripts/diff_check.py \
    <(git show HEAD:"$docs_file") "$docs_file"
  python3 /Users/mariusvniekerk/.agents/skills/unslop/scripts/validate_preservation.py \
    <(git show HEAD:"$docs_file") "$docs_file"
done
```

Review every reported URL change against the canonical `kenn-io/forge`
decision. No unrelated command, path, provider fact, or workflow claim may be
lost. Then run:

```bash
git diff --check
node node_modules/vite-plus/bin/vp exec -- node --test scripts/*.test.mjs scripts/*.test.ts
git status --short
```

Use `superpowers:verification-before-completion`. Invoke the repository-local
`context-sync --commit` workflow, then commit Task 4 with a conventional
subject and rationale-focused body.

---

### Task 5: Publish the stacked pull request

**Files:**
- No repository file changes expected

**Interfaces:**
- Consumes: the verified commits from Tasks 1 through 4 and onboarding PR #816
- Produces: a pushed branch and a pull request whose base is \`t3code/design-onboarding-mockups\`

- [ ] **Step 1: Scrub public metadata and the complete diff**

The repository references a \`scrub-private-data\` skill that is not installed in this session. Perform the fallback manually with \`git diff origin/t3code/design-onboarding-mockups...HEAD\`, \`git log\`, and targeted searches for home paths, private hosts, credentials, runner topology, and private project names. Generic seeded names such as \`acme/widgets\` are allowed.

- [ ] **Step 2: Push and create the attributed pull request**

Push `t3code/refine-onboarding-documentation` to `origin`. Create the pull
request with `gh pr create`, base `t3code/design-onboarding-mockups`, a concise
title, and only this style of user-visible summary:

```markdown
- Adds a release-first setup path and provider-aware onboarding guidance
- Organizes daily maintainer, archive, fleet, and recovery workflows
- Adds current first-run, review, workspace, and activity visuals

<sup>generated by a clanker</sup>
```

Supply `--base`, `--head`, `--title`, and `--body` so the command cannot prompt.
Capture the returned PR number. The attribution footer must be present at
creation time; do not create the PR through `gh stack link`.

- [ ] **Step 3: Link the existing pull requests**

Run:

```bash
gh stack link --base main --open --remote origin 816 "$pr_number"
```

Both arguments are existing PRs in bottom-to-top order. This links the new PR
above #816 without generating or replacing its title or body.

- [ ] **Step 4: Verify the remote stack**

Run \`gh stack view --json\` and \`gh pr view\` for the new PR. Confirm the new PR is open, its base is \`t3code/design-onboarding-mockups\`, and PR #816 remains the lower stack layer. Do not poll CI.
