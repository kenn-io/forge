# Kit UI Error Flash Consistency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route every transient user-action failure through the shared page-level kit-ui flash stack while retaining inline validation, load, conflict, and degraded-state errors.

**Architecture:** Keep the existing `@middleman/ui/stores/flash` module and one shell-owned `FlashBanner`; no new notification wrapper or feature-local banner is introduced. Action owners call `showFlash(message, { tone: "danger" })`, while mixed-purpose components keep separate inline state only for validation, unavailable content, conflicts, or recovery instructions.

**Tech Stack:** Svelte 5 runes, TypeScript, `@kenn-io/kit-ui`, Vite+ unit/browser tests, Testing Library.

## Global Constraints

- Transient user-triggered operation failures use `showFlash(message, { tone: "danger" })` from `@middleman/ui/stores/flash`.
- Initial/list/detail load failures, validation, durable conflicts, recovery instructions, and degraded service status remain inline.
- A failure appears on one surface only; migrated paths remove duplicate inline error state and markup.
- `FlashBanner` remains shell-owned and fixed below global chrome, or at `top="0"` in a headerless embed; feature components never mount it.
- Busy state must clear and retryable dialogs/editors must remain open after a flashed failure.
- Do not introduce a compatibility adapter, alias, fallback store, or application-specific notification abstraction.
- Use Bun/Vite+ commands only; never invoke npm.
- Run Svelte analysis on every edited `.svelte` file and the complete frontend `vp test` suite after the final frontend edit.

## Disposition Audit

The implementation must preserve these inline categories while migrating the action category:

| Surface | Keep inline | Move to danger flash |
| --- | --- | --- |
| Kata | bootstrap/navigation/detail/graph/daemon failures; recurrence conflict form; empty project name | task mutations, quick capture, workspace creation, project create/rename transport failures, unlink |
| Provider detail | detail/list loads, label/user candidate loads, merge/head conflicts, disabled reasons | state/workspace/label/user/comment/review/suggestion/star mutations |
| Docs | folder/tree/document/editor loads, picker browse, filename/path validation, publish conflicts/recovery, inline tree rename recovery | save/create/delete/rename transport failures, generic folder registration mutations |
| Messages | message/search/thread/facet loads, setup field validation | link-message and setup-save transport failures |
| Repositories/settings | initial preview/load failures, import pattern validation, rollback-failed recovery, field validation | saves, add/remove/refresh/import/promote/create-issue mutations |
| Workspaces | workspace/runtime loads, setup/daemon/runtime health, force-delete confirmation | retry/refresh/delete/session/project/first-run action failures |

---

### Task 1: Pin Flash Rendering to the Page and Normalize Existing Error Callers

**Files:**
- Modify: `frontend/src/App.flash-presentations.browser.svelte.ts`
- Modify: `frontend/src/App.svelte`
- Modify: `frontend/src/Provider.test.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceEmbedShell.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts`
- Modify: `frontend/src/lib/stores/keyboard/dispatch.svelte.ts`
- Modify: `frontend/src/lib/stores/keyboard/dispatch.svelte.test.ts`
- Modify: `frontend/src/lib/utils/itemRefHandler.ts`
- Modify: `frontend/src/lib/utils/itemRefHandler.test.ts`
- Modify: `packages/ui/src/Provider.svelte`

**Interfaces:**
- Consumes: `showFlash(message: string, options?: { tone?: "danger" | "warning" | "success" | "info" | "neutral" }): void`.
- Produces: shell-level geometry and semantic-tone coverage used by all later tasks.

- [ ] **Step 1: Add failing page-placement and error-tone tests**

Extend `frontend/src/App.flash-presentations.browser.svelte.ts` with a helper and assertions for desktop, compact, and embed shells:

```ts
async function visibleFlash(message: string): Promise<HTMLElement> {
  const { showFlash } = await import("@middleman/ui/stores/flash");
  showFlash(message, { tone: "danger" });
  return vi.waitFor(() => {
    const stack = document.querySelector<HTMLElement>(".kit-flash-stack");
    expect(stack?.textContent).toContain(message);
    return stack!;
  }, WAIT);
}

function expectBelowHeader(stack: HTMLElement): void {
  const header = document.querySelector<HTMLElement>(".app-top-bar");
  expect(header).not.toBeNull();
  expect(Math.abs(stack.getBoundingClientRect().top - header!.getBoundingClientRect().bottom)).toBeLessThan(1);
  expect(stack.closest(".focus-layout, .desktop-layout, .embed-layout")).toBeNull();
  expect(stack.querySelector(".kit-flash-banner")?.getAttribute("data-kit-tone")).toBe("danger");
}
```

Add one 1280px `/pulls` case, one 390px `/pulls` case, and assert `getBoundingClientRect().top === 0` for `/workspaces/embed/empty/noSelection`.

In `frontend/src/Provider.test.ts`, add a test that renders `Provider` with both callbacks, invokes `captured.store?.options.onDeferredMergeCompleted` with `status: "failed"`, and expects only `onError`:

```ts
expect(onError).toHaveBeenCalledWith("Deferred merge for acme/widget#42 failed: checks did not pass");
expect(onNotification).not.toHaveBeenCalled();
```

Update existing keyboard and item-reference expectations to include `{ tone: "danger" }`.

- [ ] **Step 2: Run the focused tests and confirm they fail**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/App.flash-presentations.browser.svelte.ts src/Provider.test.ts src/lib/components/terminal/WorkspaceListSidebar.test.ts src/lib/stores/keyboard/dispatch.svelte.test.ts src/lib/utils/itemRefHandler.test.ts
```

Expected: failures because current calls are neutral, deferred merge failures prefer `onNotification`, and compact/page geometry is not asserted yet.

- [ ] **Step 3: Apply semantic tones without adding a wrapper**

Use direct calls at each error site:

```ts
showFlash(message, { tone: "danger" });
```

Make these exact routing changes:

- `App.svelte`: linked-task failures and PR action `onError` use danger; `Provider onError` is an inline lambda using danger; `onNotification={showFlash}` stays neutral.
- `WorkspaceEmbedShell.svelte`: wrap only `Provider onError` with danger.
- `Provider.svelte`: deferred merge `status !== "merged"` calls `errorCb` directly; success continues through `notificationCb`.
- `dispatch.svelte.ts` and `itemRefHandler.ts`: every `showFlash` is an error and gets danger.
- `WorkspaceListSidebar.svelte`: copy failure, refresh, open/close/archive/delete failures, and missing provider URL get danger; the successful copy message stays non-error.

The full-shell mount remains:

```svelte
<FlashBanner top="var(--header-height)" />
```

The headerless embed mount remains:

```svelte
<FlashBanner top="0" />
```

- [ ] **Step 4: Run focused tests and Svelte analysis**

Run:

```bash
cd frontend && ./node_modules/.bin/vp test src/App.flash-presentations.browser.svelte.ts src/Provider.test.ts src/lib/components/terminal/WorkspaceListSidebar.test.ts src/lib/stores/keyboard/dispatch.svelte.test.ts src/lib/utils/itemRefHandler.test.ts
```

Expected: PASS; the flash has danger tone and page-level geometry in all three shells.

Run the Svelte analysis command from the `svelte-code-writer` skill for `App.svelte`, `WorkspaceEmbedShell.svelte`, `WorkspaceListSidebar.svelte`, and `packages/ui/src/Provider.svelte`; expected: no new findings.

- [ ] **Step 5: Commit the shell and existing-caller normalization**

```bash
git add frontend/src/App.flash-presentations.browser.svelte.ts frontend/src/App.svelte frontend/src/Provider.test.ts frontend/src/lib/components/terminal/WorkspaceEmbedShell.svelte frontend/src/lib/components/terminal/WorkspaceListSidebar.svelte frontend/src/lib/components/terminal/WorkspaceListSidebar.test.ts frontend/src/lib/stores/keyboard/dispatch.svelte.ts frontend/src/lib/stores/keyboard/dispatch.svelte.test.ts frontend/src/lib/utils/itemRefHandler.ts frontend/src/lib/utils/itemRefHandler.test.ts packages/ui/src/Provider.svelte
git commit -m "fix: keep error flashes visible at the page top"
```

---

### Task 2: Route Kata Mutations Through the Shared Danger Flash

**Files:**
- Modify: `frontend/src/lib/features/kata/KataWorkspace.svelte`
- Modify: `frontend/src/lib/features/kata/KataWorkspace.test.ts`
- Modify: `frontend/src/lib/components/kata/KataSidebar.svelte`
- Modify: `frontend/src/lib/components/kata/KataSidebar.test.ts`
- Modify: `frontend/src/lib/components/kata/KataIssueDetail.svelte`
- Modify: `frontend/src/lib/components/kata/KataIssueDetail.test.ts`
- Modify: `frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.svelte`

**Interfaces:**
- Consumes: the page-level flash mount from Task 1.
- Produces: `runViewTask(task, surface)` where `surface` explicitly distinguishes `"daemon"`, `"flash"`, and `"none"`; Kata mutation callbacks continue returning the same booleans/promises.

- [ ] **Step 1: Change Kata tests to require danger flashes and retained inline loads**

In Kata test files, import the module namespace and clear it between tests:

```ts
import * as flash from "@middleman/ui/stores/flash";

afterEach(() => {
  for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
});
```

Replace `"surfaces task request failures outside the daemon switcher"` so it asserts:

```ts
await waitFor(() => {
  expect(flash.getFlash()).toMatchObject({ message: "owner unavailable", tone: "danger" });
});
expect(screen.queryByText("owner unavailable")).toBeNull();
expect(ownerInput.value).toBe("agent:new");
```

Add failures for workspace creation, project creation, and project rename that assert danger flashes, preserved controls/drafts, and no `.kata-request-error`, `.sidebar-error`, or `.unlink-error` transport message. Keep the existing `detail failed` and bootstrap tests asserting inline `role="alert"` because those are unavailable-content states.

- [ ] **Step 2: Run Kata tests and confirm the new expectations fail**

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/features/kata/KataWorkspace.test.ts src/lib/components/kata/KataSidebar.test.ts src/lib/components/kata/KataIssueDetail.test.ts
```

Expected: failures because task/project/workspace/unlink operations still populate local error elements.

- [ ] **Step 3: Split Kata load and mutation error paths**

In `KataWorkspace.svelte`, replace the request surface with an explicit flash surface:

```ts
type FailureSurface = "flash" | "daemon" | "none";

function surfaceTaskError(message: string, surface: FailureSurface): void {
  lastTaskError = message;
  if (surface === "flash") {
    showFlash(message, { tone: "danger" });
  } else if (surface === "daemon") {
    error = message;
  }
}
```

Use `"daemon"` for bootstrap, view/filter/scope/detail selection, and daemon switching. Pass `"flash"` for task edits, comments, ownership, priority, labels, close/reopen/delete, quick capture, workspace creation, and unlink. Keep recurrence create/patch on `"none"` because the recurrence workflow owns inline conflict/recovery state.

Remove `requestError`, `unlinkError`, their rendered elements, and the `unlinkError` prop from `KataIssueDetail.svelte`. The unlink callback returns to idle after `runViewTask(..., "flash")` reports failure.

In `KataSidebar.svelte`, retain `renameError` only for `"Project name can't be empty."`; API catches become:

```ts
showFlash(err instanceof Error ? err.message : "Could not rename project.", { tone: "danger" });
```

Remove `createError` entirely because creation has no separate validation message.

In `KataWorkspaceSidebarPane.svelte`, keep a dedicated `loadError` for bootstrap/selection. Replace mutation `runTask` catches with danger flashes, keep recurrence throws inline in their dialog, and remove unlink-specific error state/props.

- [ ] **Step 4: Run Kata tests and analyze every edited Svelte component**

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/features/kata/KataWorkspace.test.ts src/lib/components/kata/KataSidebar.test.ts src/lib/components/kata/KataIssueDetail.test.ts
```

Expected: PASS; action failures are in the shared flash store and load failures remain inline.

Run Svelte analysis for the four edited components; expected: no new findings.

- [ ] **Step 5: Commit the Kata migration**

```bash
git add frontend/src/lib/features/kata/KataWorkspace.svelte frontend/src/lib/features/kata/KataWorkspace.test.ts frontend/src/lib/components/kata/KataSidebar.svelte frontend/src/lib/components/kata/KataSidebar.test.ts frontend/src/lib/components/kata/KataIssueDetail.svelte frontend/src/lib/components/kata/KataIssueDetail.test.ts frontend/src/lib/components/terminal/KataWorkspaceSidebarPane.svelte
git commit -m "fix: surface Kata action failures through kit-ui"
```

---

### Task 3: Separate Provider Load Errors from Mutation Flashes

**Files:**
- Modify: `packages/ui/src/stores/detail.svelte.ts`
- Modify: `packages/ui/src/stores/detail.svelte.test.ts`
- Modify: `packages/ui/src/stores/issues.svelte.ts`
- Modify: `packages/ui/src/stores/pulls.svelte.ts`
- Modify: `packages/ui/src/stores/pulls.svelte.test.ts`
- Modify: `packages/ui/src/stores/activity.svelte.ts`
- Modify: `packages/ui/src/stores/activity.svelte.test.ts`
- Modify: `packages/ui/src/components/detail/PullDetail.svelte`
- Modify: `packages/ui/src/components/detail/PullDetail.test.ts`
- Modify: `packages/ui/src/components/detail/IssueDetail.svelte`
- Modify: `packages/ui/src/components/detail/IssueDetail.test.ts`
- Modify: `packages/ui/src/components/detail/CommentBox.svelte`
- Modify: `packages/ui/src/components/detail/IssueCommentBox.svelte`
- Modify: `packages/ui/src/components/detail/comment-drafts.svelte.ts`
- Modify: `packages/ui/src/components/detail/EventTimeline.svelte`
- Modify: `packages/ui/src/components/detail/EventTimeline.test.ts`
- Modify: `packages/ui/src/components/detail/UserListEditor.svelte`
- Modify: `packages/ui/src/components/detail/ReadyForReviewButton.svelte`
- Modify: `packages/ui/src/components/detail/ApproveButton.svelte`
- Modify: `packages/ui/src/components/detail/ApproveButton.svelte.test.ts`
- Modify: `packages/ui/src/components/detail/ApproveWorkflowsButton.svelte`
- Modify: `packages/ui/src/components/detail/MergeModal.svelte`
- Modify: `packages/ui/src/components/detail/MergeModal.svelte.test.ts`

**Interfaces:**
- Consumes: the shared danger flash.
- Produces: `detail.submitComment(...) => Promise<boolean>` and `issues.submitIssueComment(...) => Promise<boolean>` so editors preserve drafts without consulting load-error state.

- [ ] **Step 1: Add store-level mutation flash tests**

Add test setup using the real shared store:

```ts
import { dismissFlash, getFlash, getFlashes } from "./flash.svelte.js";

afterEach(() => {
  for (const item of getFlashes()) dismissFlash(item.id);
});
```

Cover one optimistic mutation rollback in each store family:

```ts
expect(getFlash()).toMatchObject({ message: "permission denied", tone: "danger" });
expect(store.getDetailError()).toBeNull(); // issue/detail load state was not poisoned
```

For activity `markNotificationSeen`, retain the optimistic rollback assertion and additionally require the danger flash. Update CI refresh tests to require danger for request failure and warning tone for server warnings.

Add component assertions that a failed state/workspace/ready/approve/workflow action produces a danger flash and no `.action-error`, `.ready-error`, `.approve-error`, or `.workflow-approval-error`. Keep label catalog/candidate load errors inline.

- [ ] **Step 2: Run provider store/detail tests and confirm failures**

```bash
cd frontend && ./node_modules/.bin/vp test ../packages/ui/src/stores/detail.svelte.test.ts ../packages/ui/src/stores/pulls.svelte.test.ts ../packages/ui/src/stores/activity.svelte.test.ts ../packages/ui/src/components/detail/PullDetail.test.ts ../packages/ui/src/components/detail/IssueDetail.test.ts ../packages/ui/src/components/detail/ApproveButton.svelte.test.ts ../packages/ui/src/components/detail/MergeModal.svelte.test.ts
```

Expected: failures because mutation errors still populate store/detail-local error state or neutral flashes.

- [ ] **Step 3: Move provider mutations to danger flashes**

In `detail.svelte.ts`, `issues.svelte.ts`, `pulls.svelte.ts`, and `activity.svelte.ts`, preserve `storeError`/`detailError` only for list/detail reads. Mutation failure branches use:

```ts
const message = err instanceof Error ? err.message : String(err);
showFlash(message, { tone: "danger" });
```

Keep optimistic rollback and boolean return behavior unchanged. Convert `submitComment` and `submitIssueComment` to return `false` after flashing and `true` after refresh scheduling.

Update `CommentBox.svelte` and `IssueCommentBox.svelte` to clear drafts only when the returned boolean is true:

```ts
const posted = await detail.submitComment(submittedOwner, submittedName, submittedNumber, submittedBody);
if (posted) clearCommentDraft("pull", submittedOwner, submittedName, submittedNumber, submittedPlatformHost);
```

Remove submit-error storage and exports from `comment-drafts.svelte.ts`; disabled-operation reasons remain inline under the editor.

For `PullDetail.svelte` and `IssueDetail.svelte`:

- state and workspace transport failures flash and remove `stateError`, `wsError`, and `workspaceError` markup;
- branch/head/merge conflicts stay inline;
- label catalog load continues to set `labelPickerError`, while label mutation failure flashes;
- user candidate load remains inline, while `UserListEditor` mutation failure flashes.

For action components and timeline editors, keep the editor/popover open and replace local mutation errors with danger flashes. `MergeModal` keeps generic provider merge conflicts inline but flashes non-conflict transport failures:

```ts
if (isProblem(requestError) && problemConflictReason(requestError) === "conflict") {
  error = requestError.detail ?? requestError.title ?? "failed to merge pull request";
} else {
  showFlash(requestError.detail ?? requestError.title ?? "failed to merge pull request", { tone: "danger" });
}
```

- [ ] **Step 4: Run provider tests and Svelte analysis**

Run the focused command from Step 2 plus:

```bash
cd frontend && ./node_modules/.bin/vp test ../packages/ui/src/components/detail/EventTimeline.test.ts
```

Expected: PASS; load/conflict states remain inline and mutations use danger flashes.

Run Svelte analysis for every edited component in this task; expected: no new findings.

- [ ] **Step 5: Commit provider action error consistency**

```bash
git add packages/ui/src/stores packages/ui/src/components/detail
git commit -m "fix: separate provider action errors from load states"
```

---

### Task 4: Migrate Docs, Messages, Repository, and Settings Actions

**Files:**
- Modify: `frontend/src/lib/components/docs/AddFolderDialog.svelte`
- Modify: `frontend/src/lib/components/docs/AddFolderDialog.test.ts`
- Modify: `frontend/src/lib/components/docs/DocsWorkspace.svelte`
- Modify: `frontend/src/lib/components/docs/DocsWorkspace.test.ts`
- Modify: `frontend/src/lib/components/messages/MessageDetail.svelte`
- Modify: `frontend/src/lib/components/messages/MessageDetail.test.ts`
- Modify: `frontend/src/lib/components/messages/MessagesSetupDialog.svelte`
- Modify: `frontend/src/lib/components/messages/MessagesSetupDialog.test.ts`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryPage.svelte`
- Modify: `frontend/src/lib/components/repositories/RepoSummaryPage.test.ts`
- Modify: `frontend/src/lib/components/settings/ActivitySettings.svelte`
- Modify: `frontend/src/lib/components/settings/AgentSettings.svelte`
- Modify: `frontend/src/lib/components/settings/AgentSettings.test.ts`
- Modify: `frontend/src/lib/components/settings/FleetSettings.svelte`
- Modify: `frontend/src/lib/components/settings/FleetSettings.test.ts`
- Modify: `frontend/src/lib/components/settings/KataProjectMappingsSettings.svelte`
- Modify: `frontend/src/lib/components/settings/KataProjectMappingsSettings.test.ts`
- Modify: `frontend/src/lib/components/settings/ModeVisibilitySettings.svelte`
- Modify: `frontend/src/lib/components/settings/ModeVisibilitySettings.test.ts`
- Modify: `frontend/src/lib/components/settings/RepoImportModal.svelte`
- Modify: `frontend/src/lib/components/settings/RepoImportModal.test.ts`
- Modify: `frontend/src/lib/components/settings/RepoPromoteModal.svelte`
- Modify: `frontend/src/lib/components/settings/RepoSettings.svelte`
- Modify: `frontend/src/lib/components/settings/RepoSettings.test.ts`
- Modify: `frontend/src/lib/components/settings/TerminalSettings.svelte`
- Modify: `frontend/src/lib/components/settings/TerminalSettings.test.ts`

**Interfaces:**
- Consumes: `showFlash` and the disposition table above.
- Produces: no new shared API; mixed forms retain their existing validation variables only.

- [ ] **Step 1: Update representative tests to distinguish validation/load from transport failures**

For each mixed form, add both sides of the boundary. Example for `AddFolderDialog.test.ts`:

```ts
expect(screen.getByRole("alert").textContent).toContain("id taken"); // typed duplicate remains inline

vi.spyOn(api, "addFolder").mockRejectedValue(new Error("daemon unavailable"));
await fireEvent.click(screen.getByRole("button", { name: "Add folder" }));
await waitFor(() => expect(flash.getFlash()).toMatchObject({
  message: "daemon unavailable",
  tone: "danger",
}));
expect(screen.queryByText("daemon unavailable")).toBeNull();
expect(onClose).not.toHaveBeenCalled();
```

Add equivalent tests for document save, message linking, settings save, repo refresh, import submit, and issue creation. Retain existing tests for folder browsing, message/search loads, repo preview failures, validation, and rollback-failed recovery.

- [ ] **Step 2: Run the focused application-mode tests and confirm failures**

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/components/docs/AddFolderDialog.test.ts src/lib/components/docs/DocsWorkspace.test.ts src/lib/components/messages/MessageDetail.test.ts src/lib/components/messages/MessagesSetupDialog.test.ts src/lib/components/repositories/RepoSummaryPage.test.ts src/lib/components/settings/AgentSettings.test.ts src/lib/components/settings/FleetSettings.test.ts src/lib/components/settings/KataProjectMappingsSettings.test.ts src/lib/components/settings/ModeVisibilitySettings.test.ts src/lib/components/settings/RepoImportModal.test.ts src/lib/components/settings/RepoSettings.test.ts src/lib/components/settings/TerminalSettings.test.ts
```

Expected: new transport/action assertions fail while existing inline load/validation tests pass.

- [ ] **Step 3: Implement per-path classification**

Use `showFlash(..., { tone: "danger" })` for generic transport/action failures and retain inline state for field-specific codes. In Docs, keep these API codes inline because the user must correct the named input:

```ts
function isFileInputError(err: unknown): boolean {
  const code = (err as DocsAPIError | undefined)?.code;
  return code === "already_exists" || code === "unsupported_extension" || code === "outside_folder";
}
```

`PublishDocsDialog.svelte` remains unchanged: its conflict and post-commit push recovery text is durable workflow state. `handleInlineRename` also remains inline because it explains and repairs the tree's optimistic rename.

Apply these exact mixed-state rules:

- `AddFolderDialog`: missing path and typed duplicate-id errors inline; generic add failure danger flash; browse failure inline.
- `DocsWorkspace`: local dirty-edit/name/path validation inline; typed file-input API errors inline; generic save/create/rename/delete/folder mutation failures danger flash.
- `MessageDetail`: link failure danger flash; message load/sanitization state inline.
- `MessagesSetupDialog`: URL/env validation inline; save transport failure danger flash; dialog stays open.
- `RepoSummaryPage`: initial summary load inline; refresh/create-issue action failure danger flash; title-required inline.
- Settings save components: replace local save errors or console-only catches with danger flashes and preserve drafts/rollback behavior.
- `RepoSettings`: format validation inline; add/remove/refresh/worktree-save API failures danger flash.
- `RepoImportModal`: pattern/preview errors inline; bulk-add submit failure danger flash.
- `RepoPromoteModal`: match load inline; simple promote failure danger flash; rollback-failed message remains inline because manual recovery may be required.

- [ ] **Step 4: Run focused tests and Svelte analysis**

Run the command from Step 2; expected: PASS.

Run Svelte analysis for every edited component in this task; expected: no new findings.

- [ ] **Step 5: Commit application-mode action flashes**

```bash
git add frontend/src/lib/components/docs frontend/src/lib/components/messages frontend/src/lib/components/repositories/RepoSummaryPage.svelte frontend/src/lib/components/repositories/RepoSummaryPage.test.ts frontend/src/lib/components/settings
git commit -m "fix: standardize application action error flashes"
```

---

### Task 5: Migrate Workspace and Terminal Action Failures

**Files:**
- Modify: `frontend/src/lib/components/terminal/WorkspaceFirstRunPanel.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceFirstRunPanel.test.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceProjectCard.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceProjectCard.test.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts`
- Modify: `frontend/src/lib/components/terminal/WorkspaceTerminalViewEmbed.test.ts`

**Interfaces:**
- Consumes: page-level flashes in both full and embedded shells.
- Produces: `runtimeError` reserved for runtime availability/degraded state; no `actionError` inline state.

- [ ] **Step 1: Add failing workspace action-flash tests**

Update existing first-run, project-card, and terminal tests to spy on the shared flash and assert danger tone. Representative terminal expectation:

```ts
await waitFor(() => expect(mocks.showFlash).toHaveBeenCalledWith(
  "Refresh failed (503)",
  { tone: "danger" },
));
expect(container.querySelector(".action-error")).toBeNull();
```

Keep tests asserting `loadError`, `runtimeError` from runtime fetch, force-delete confirmation, and setup/daemon state inline. Add an action failure for launch/stop/rename that verifies the existing runtime data remains visible while the danger flash is raised.

- [ ] **Step 2: Run focused workspace tests and confirm failures**

```bash
cd frontend && ./node_modules/.bin/vp test src/lib/components/terminal/WorkspaceFirstRunPanel.test.ts src/lib/components/terminal/WorkspaceProjectCard.test.ts src/lib/components/terminal/WorkspaceTerminalView.test.ts src/lib/components/terminal/WorkspaceTerminalViewEmbed.test.ts
```

Expected: failures because several actions still set `lastError`, `actionError`, or `runtimeError` inline and existing flash calls are neutral.

- [ ] **Step 3: Split workspace load/health and action failures**

Use danger flashes for:

- first-run register/clone/GitHub clone/host-command failures;
- project-card missing host action and failed action acknowledgement;
- workspace retry, refresh, non-conflict delete, force-delete, session launch/stop/rename, terminal launch, and preset launch failures.

Keep inline:

- first-run repository-list load failure;
- workspace and runtime fetch failures;
- workspace setup/daemon degraded state;
- force-delete 409 confirmation and its recovery text.

Remove `actionError` and its markup from `WorkspaceTerminalView.svelte`. Do not clear or overwrite `runtimeError` from action handlers; action handlers call:

```ts
showFlash(err instanceof Error ? err.message : "Launch failed", { tone: "danger" });
```

`WorkspaceFirstRunPanel` keeps a dedicated repository-list load error but no shared `lastError` for submissions. `WorkspaceProjectCard` keeps `loadError` and removes `actionError`.

- [ ] **Step 4: Run workspace tests and Svelte analysis**

Run the command from Step 2; expected: PASS.

Run Svelte analysis for all three edited components; expected: no new findings.

- [ ] **Step 5: Commit workspace action error migration**

```bash
git add frontend/src/lib/components/terminal/WorkspaceFirstRunPanel.svelte frontend/src/lib/components/terminal/WorkspaceFirstRunPanel.test.ts frontend/src/lib/components/terminal/WorkspaceProjectCard.svelte frontend/src/lib/components/terminal/WorkspaceProjectCard.test.ts frontend/src/lib/components/terminal/WorkspaceTerminalView.svelte frontend/src/lib/components/terminal/WorkspaceTerminalView.test.ts frontend/src/lib/components/terminal/WorkspaceTerminalViewEmbed.test.ts
git commit -m "fix: show workspace action failures at the page top"
```

---

### Task 6: Complete the Error-Surface Audit and Full Verification

**Files:**
- Verify only; no file changes are expected. If the audit finds a missed path, return to its owning task, add the focused failing test there, and repeat that task's implementation and commit steps before restarting Task 6.

**Interfaces:**
- Consumes: all prior task behavior.
- Produces: verified application-wide consistency with zero duplicate local action errors.

- [ ] **Step 1: Re-run the source audit and classify every remaining local error**

Run:

```bash
rg -n --glob '*.svelte' 'let [A-Za-z0-9_]*(Error|error)[A-Za-z0-9_]*\s*=\s*\$state|role="alert"|class="[^"]*error' frontend/src packages/ui/src
rg -n --glob '*.svelte' --glob '*.svelte.ts' --glob '*.ts' 'showFlash\(' frontend/src packages/ui/src
```

Expected: each remaining inline error maps to load, validation, conflict/recovery, or degraded-state rows in the disposition audit; every error `showFlash` call includes `{ tone: "danger" }`. Success/notification calls may remain neutral or use their existing semantic tone.

- [ ] **Step 2: Run kit-ui and frontend checks**

```bash
make frontend-check
```

Expected: format, lint, TypeScript/Svelte, and kit-ui checks pass with zero findings.

- [ ] **Step 3: Run the complete frontend test suite**

```bash
cd frontend && ./node_modules/.bin/vp test
```

Expected: complete unit and browser suite passes.

- [ ] **Step 4: Run context-sync and inspect the final diff**

```bash
scripts/context-sync --check
git status --short
git diff --check
git diff --stat origin/main...HEAD
```

Expected: context-sync preflight passes, no whitespace errors, and only the approved spec/context plus implementation/test changes appear.
