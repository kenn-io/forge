# Provider Readiness Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put a provider-aware code-forge readiness screen before repository selection, while preserving GitHub discovery and resuming configured non-GitHub repositories at first sync.

**Approved spec/design:** `docs/superpowers/specs/2026-08-02-provider-readiness-onboarding-design.md`

**Architecture:** Keep `OnboardingFlow.svelte` responsible for the five-step workflow and extract the new first-step presentation into a focused `ProviderReadinessStep.svelte`. `OnboardingFlow` owns the transient GitHub confirmation, provider-settings handoff, repository discovery, and phase transitions; the readiness child receives normalized status and emits explicit actions. Existing settings remains the single provider configuration surface.

**Tech Stack:** Svelte 5 runes, TypeScript, `@kenn-io/kit-ui`, Vitest with Testing Library, Playwright against the isolated Go e2e server.

## Global Constraints

- Show `Connect a code forge` whenever onboarding starts with no configured repositories, even when `gh` is authenticated.
- Keep five milestones and rename the first progress label to `Code forge`.
- GitHub is the only inline repository-discovery path; GitLab, Forgejo, and Gitea hand off to Repositories settings.
- Settings handoff keeps onboarding `active`; do not call `onDismiss` or `onComplete`.
- A configured repository resumes at first sync with its complete `(provider, platform_host, owner, name)` identity.
- Unknown tooling, probe failure, missing CLI, unauthenticated CLI, and repository-discovery failure remain recoverable non-crashing states.
- Keep at least `var(--space-5)` between readiness statuses and actions, including when no command or inline error is rendered.
- Provider rows and actions stack without horizontal overflow at narrow widths.
- Repository discovery requests the server's bounded `limit: 1000` maximum.
- Do not add a compatibility migration for the unshipped localStorage `"dismissed"` value.
- Use only shared spacing tokens and existing kit/provider primitives; do not introduce a local badge or button system.
- Do not add credential APIs, browser-driven CLI installation, or duplicate provider token/host forms.

---

### Task 1: Provider readiness checkpoint and workflow state

**Files:**

- Create: `frontend/src/lib/components/onboarding/ProviderReadinessStep.svelte`
- Modify: `frontend/src/lib/components/onboarding/OnboardingFlow.svelte`
- Test: `frontend/src/lib/components/onboarding/OnboardingFlow.test.ts`
- Test: `frontend/tests/e2e-full/onboarding.spec.ts`

**Interfaces:**

- Consumes: `ToolingStatusValue` from `frontend/src/lib/stores/embed-config.svelte.ts`, existing `ProviderIcon`, kit `Button`/`Spinner`, and callbacks owned by `OnboardingFlow`.
- Produces: `ProviderReadinessStep` props `{ tooling, retrying, retryError, onContinueGitHub, onCheckAgain, onOpenSettings }`; `OnboardingFlow` local `providerConfirmed: boolean`; provider-neutral `Code forge` milestone and readiness sidebar status.

- [x] **Step 1: Write focused failing component tests**

Update `OnboardingFlow.test.ts` so the authenticated default fixture first asserts the new heading and no discovery, then clicks the explicit GitHub continue action:

```ts
expect(screen.getByRole("heading", { name: "Connect a code forge" })).toBeTruthy();
expect(mocks.listUserRepositories).not.toHaveBeenCalled();
await fireEvent.click(screen.getByRole("button", { name: "Continue with GitHub" }));
await waitFor(() => expect(screen.getByRole("heading", { name: "Choose the repositories you maintain" })).toBeTruthy());
```

Add separate tests whose observable contracts catch these regressions:

```ts
it("keeps a missing gh installation on the provider readiness step", async () => {
  mocks.tooling.value = {
    git: { available: true, version: "2.50" },
    gh: { available: false, authenticated: false },
    glab: { available: false, authenticated: false },
  };
  renderFlow(storeFixture().stores);

  expect(screen.getByRole("heading", { name: "Connect a code forge" })).toBeTruthy();
  expect(screen.getByText("gh is not installed")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Check again" })).toBeTruthy();
  expect(mocks.listUserRepositories).not.toHaveBeenCalled();
});

it("keeps onboarding active while repositories settings configures another forge", async () => {
  const callbacks = renderFlow(storeFixture().stores);
  await fireEvent.click(screen.getByRole("button", { name: "Configure Forgejo" }));

  expect(callbacks.onDismiss).not.toHaveBeenCalled();
  expect(callbacks.onComplete).not.toHaveBeenCalled();
  expect(mocks.navigate).toHaveBeenCalledWith("/settings");
});

it("resumes configured repositories at first sync without provider confirmation", async () => {
  const fixture = storeFixture({ configured: true });
  renderFlow(fixture.stores);

  await waitFor(() => expect(fixture.triggerSync).toHaveBeenCalledOnce());
  expect(screen.queryByRole("heading", { name: "Connect a code forge" })).toBeNull();
  expect(mocks.listUserRepositories).not.toHaveBeenCalled();
});
```

Update existing repository-path tests to click `Continue with GitHub` before expecting discovery, update the milestone assertion to `Code forge`, and assert that the repository picker offers `Configure repositories in Settings` without dismissing onboarding.

- [x] **Step 2: Write the real-backend settings-handoff regression before implementation**

In `prepareGitHubOnboarding`, assert the discovery request includes `limit=1000`. In `configureWidgetRepository`, require `Connect a code forge`, click `Continue with GitHub`, then continue with the existing repository selection. Update the mobile missing-tool scenario to expect `Connect a code forge`, `Code forge: current`, all four provider rows, and a `Configure Forgejo` settings handoff.

Add one test that starts an isolated server, removes configured repositories, resets onboarding state, opens `/`, clicks `Configure Forgejo`, and confirms `/settings` opens without writing dismissed state:

```ts
expect(await page.evaluate(() => localStorage.getItem("kenn-forge:first-run-onboarding"))).toBe("active");
```

Through the real Repositories settings UI, open `Add repositories…`, select Forgejo, keep host `codeberg.org`, preview `forge-lab/*`, add `forge-lab/service`, and click `Back to app`. Wait for `First sync is underway`, then query `/api/v1/settings` and poll `/api/v1/repos` until both surfaces retain the literal identity. The seeded static provider has no nonzero repository ID, so the fixture exercises the recoverable sync-error state rather than advancing to pull selection:

```ts
expect(settings.repos).toContainEqual(
  expect.objectContaining({
    provider: "forgejo",
    platform_host: "codeberg.org",
    owner: "forge-lab",
    name: "service",
    repo_path: "forge-lab/service",
  }),
);
await expect
  .poll(async () => {
    const response = await page.request.get(`${server!.info.base_url}/api/v1/repos`);
    const repos = (await response.json()) as RepoSummary[];
    return repos.some(
      (repo) =>
        repo.Platform === "forgejo" &&
        repo.PlatformHost === "codeberg.org" &&
        repo.Owner === "forge-lab" &&
        repo.Name === "service",
    );
  })
  .toBe(true);
```

This checks the application-owned handoff and identity seam; do not duplicate provider-client transport coverage or assert incidental settings markup.

- [x] **Step 3: Run both focused contracts and verify the expected failures**

Run from the repository root:

```bash
(cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/components/onboarding/OnboardingFlow.test.ts)
(cd frontend && ../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts --project=chromium tests/e2e-full/onboarding.spec.ts)
```

Expected: FAIL because the authenticated session currently skips the readiness page, the heading/milestone/provider actions do not exist, and settings handoff currently calls `onDismiss`.

- [x] **Step 4: Implement the focused readiness component**

Create `ProviderReadinessStep.svelte` in Svelte 5 runes mode. Render one compact list with keyed GitHub, GitLab, Forgejo, and Gitea rows. Use `ProviderIcon` for provider identity, status text in every row, and provider-specific actions:

```svelte
<script lang="ts">
  import type { ToolingStatusValue } from "../../stores/embed-config.svelte.ts";

  interface Props {
    tooling: ToolingStatusValue | undefined;
    retrying: boolean;
    retryError: string | null;
    onContinueGitHub: () => void;
    onCheckAgain: () => void;
    onOpenSettings: () => void;
  }

  let {
    tooling,
    retrying,
    retryError,
    onContinueGitHub,
    onCheckAgain,
    onOpenSettings,
  }: Props = $props();

  const gh = $derived(tooling?.gh);
  const glab = $derived(tooling?.glab);
  const ghReady = $derived(gh?.available === true && gh.authenticated === true);
</script>
```

The status list contains GitHub, GitLab, Forgejo, and Gitea rows with icons and text only. A separate sibling `.readiness-actions` block exposes `Continue with GitHub` only when authenticated and `Check again` otherwise, followed by `Configure GitLab`, `Configure Forgejo`, and `Configure Gitea`. All non-GitHub buttons call the same `onOpenSettings` callback because settings owns provider selection and credentials.

The component's outer structure must keep `.provider-statuses` and `.readiness-actions` as sibling vertical blocks with:

```css
.provider-readiness {
  display: grid;
  gap: var(--space-5);
}

.provider-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: var(--space-4);
  align-items: center;
}

.readiness-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
}

@media (max-width: 640px) {
  .readiness-actions {
    align-items: stretch;
    flex-direction: column;
  }
}
```

Use the established design tokens for borders, surfaces, text, warning/success color, radii, and focus; semantic color must accompany explicit status text.

- [x] **Step 5: Integrate the transient confirmation into `OnboardingFlow`**

Change the phase model so an empty install starts at the readiness step regardless of `gh` state:

```ts
let providerConfirmed = $state(false);
const hasConfiguredRepos = $derived(stores.settings.hasConfiguredRepos());
const activeStep = $derived<StepId>(hasConfiguredRepos || providerConfirmed ? phase : "auth");

async function continueWithGitHub(): Promise<void> {
  providerConfirmed = true;
  await tick();
  headingEl?.focus();
}

function configureAnotherProvider(): void {
  navigate("/settings");
}
```

Keep `ghReady` limited to discovery readiness and retry behavior, rather than using it to select the first step. Gate automatic `loadRepositories()` on `phase === "repos" && providerConfirmed && ghReady`. A successful `Check again` sets both `ghVerified` and `providerConfirmed`, preserving the current newly-authenticated recovery path. Replace the inline GitHub-only first-step markup with `ProviderReadinessStep`, change the first milestone label to `Code forge`, and make the sidebar identity provider-neutral (`Code forge ready` / `Code forge setup needed`) without implying non-GitHub discovery. Add `Configure repositories in Settings` beside the repository picker actions; it calls `configureAnotherProvider` and therefore preserves the active lifecycle.

- [x] **Step 6: Run Svelte analysis and focused tests**

Run:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/onboarding/ProviderReadinessStep.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/onboarding/OnboardingFlow.svelte
(cd frontend && ../node_modules/.bin/vp test run --project unit src/lib/components/onboarding/OnboardingFlow.test.ts)
(cd frontend && ../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts --project=chromium tests/e2e-full/onboarding.spec.ts)
```

Expected: both autofixers report no actionable findings; the focused unit and Chromium onboarding files pass, including mobile missing-tool and Forgejo settings-resume coverage.

- [x] **Step 7: Commit the readiness checkpoint and browser contract**

Run the repository-local context synchronization and mandatory commit workflow, then commit only the component, workflow, test, and any required context update:

```bash
git add frontend/src/lib/components/onboarding/ProviderReadinessStep.svelte \
  frontend/src/lib/components/onboarding/OnboardingFlow.svelte \
  frontend/src/lib/components/onboarding/OnboardingFlow.test.ts \
  frontend/tests/e2e-full/onboarding.spec.ts
git commit -m "feat: make code forge readiness explicit"
```

Do not amend prior commits and do not bypass hooks.

### Task 2: Final verification, captures, and PR update

**Files:**

- Verify: `frontend/src/lib/components/onboarding/ProviderReadinessStep.svelte`
- Verify: `frontend/src/lib/components/onboarding/OnboardingFlow.svelte`
- Verify: `frontend/src/lib/components/onboarding/OnboardingFlow.test.ts`
- Verify: `frontend/tests/e2e-full/onboarding.spec.ts`
- Capture outside Git: isolated missing-tool and authenticated GitHub screenshots

**Interfaces:**

- Consumes: final source tree, isolated Playwright backend, `capture-playwright`, `gh image`, and existing PR #816.
- Produces: full local verification evidence and updated PR visuals without live user data.

- [x] **Step 1: Run final Svelte, unit, check, e2e, and build gates**

After the final source/test edit, run from the repository root:

```bash
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/onboarding/ProviderReadinessStep.svelte
./node_modules/.bin/vp exec -- svelte-mcp svelte-autofixer frontend/src/lib/components/onboarding/OnboardingFlow.svelte
(cd frontend && ../node_modules/.bin/vp test run)
./node_modules/.bin/vp run frontend-check
(cd frontend && ../node_modules/.bin/vp exec -- playwright test --config=playwright-e2e.config.ts --project=chromium tests/e2e-full/onboarding.spec.ts)
(cd frontend && ../node_modules/.bin/vp build --logLevel warn)
```

Expected: no actionable Svelte findings; full Vitest, frontend checks, affected Playwright, and production build all pass.

- [x] **Step 2: Capture isolated readiness and repository-selection states**

Use the `capture-playwright` skill and the isolated e2e server only. Capture a 390px-wide missing-`gh` `Connect a code forge` screen and an authenticated desktop `Choose the repositories you maintain` screen using synthetic `acme/widgets` data. Save outside tracked docs/assets and verify both images visually before upload.

- [ ] **Step 3: Update the existing PR**

Run the required public-repository private-data scrub, push the current branch without changing branches, upload the approved isolated screenshots with `gh image`, and update PR #816's concise bulleted description with the new provider-readiness behavior and visual links. Any agent-authored GitHub text must end with:

```html
<sup>generated by a clanker</sup>
```

Do not add a test plan, implementation detail checklist, or marketing language to the PR description. Do not poll GitHub Actions unless the user explicitly asks.

- [ ] **Step 4: Report completion**

Report the PR URL, pushed commit IDs, exact verification results, and screenshot paths/links. Mention any skipped environment-dependent scenario explicitly.
