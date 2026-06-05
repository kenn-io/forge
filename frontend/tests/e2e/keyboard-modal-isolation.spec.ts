import { expect, test, type Page } from "@playwright/test";

import { mockApi } from "./support/mockApi";

// Each modal-opener entry navigates to a route and opens the modal,
// or, when the open path is too complex to script here, leaves a
// `TODO` note and skips the rest of the test body (the dispatch
// isolation assertion stays the same in every row).
//
// The shared assertion: with a modal pushed onto the keyboard
// modal-stack, a background `j` keypress must not change PR list
// selection. Selection state is observed via `.pr-list-row.selected`
// — counting matches before and after the keypress catches both the
// "no selection -> first row" and "row N -> row N+1" transitions
// that the unguarded `j` shortcut would cause.
type ModalOpener = {
  name: string;
  open: (page: Page) => Promise<void>;
  // When set, the modal-open helper above could not actually mount
  // the modal in this harness; the assertion is left in place but
  // marked skipped so the row still appears in the table and gets
  // filled in once the fixture lands.
  todo?: string;
};

async function openMergeModal(page: Page): Promise<void> {
  await page.goto("/pulls/detail?provider=github&platform_host=github.com&repo_path=acme%2Fwidgets&number=42");
  // Wait for the PR detail to render before clicking Merge.
  await expect(page.locator(".detail-title")).toContainText("Add browser regression coverage");
  const mergeButton = page.locator(".btn--merge").first();
  await expect(mergeButton).toBeVisible();
  await mergeButton.click();
  await expect(page.locator(".modal-title", { hasText: "Merge Pull Request" })).toBeVisible();
}

async function openRepoImportModal(page: Page): Promise<void> {
  await page.goto("/settings");
  // RepoSettings renders an "Add repositories…" trigger that opens
  // the import modal. The default mockApi serves a settings repo so
  // the page does not redirect to a first-run setup screen.
  const trigger = page.getByRole("button", {
    name: /add repositor/i,
  });
  await expect(trigger).toBeVisible();
  await trigger.click();
}

const MODAL_OPENERS: ModalOpener[] = [
  { name: "merge", open: openMergeModal },
  { name: "repo-import", open: openRepoImportModal },
  {
    name: "repo-issue",
    open: async (_page) => {
      // TODO: set up modal-open fixture. Mount /repos with a
      // RepoSummary fixture whose `repo.capabilities.issue_mutation`
      // is true, then click the per-card "New issue" button to open
      // RepoIssueModal. Default mockApi does not yet serve
      // `/api/v1/repos/summary`, so this row needs:
      //   1. A fixture summary list with at least one issue-mutation
      //      capability.
      //   2. A page.goto("/repos") + click on the card's
      //      "New issue" ActionButton.
      // Once the fixture exists, replace the throw with the open
      // sequence. Keep the dispatch-isolation assertion below.
      throw new Error("repo-issue modal fixture not implemented");
    },
    todo: "RepoSummary fixture + click 'New issue' on a repo card",
  },
  {
    name: "issue-detail-confirm",
    open: async (_page) => {
      // TODO: set up modal-open fixture. The IssueDetail confirm
      // sub-modal is gated by a 409-style branch-conflict response
      // from `POST .../issues/{n}/workspace`. Reproducing it
      // requires:
      //   1. Navigate to an issue detail.
      //   2. Mock the workspace POST to return a 409 with
      //      `errors[].location = body.git_head_ref` (and a
      //      suggested branch).
      //   3. Click "Create workspace" so `parseBranchConflict`
      //      promotes the error into `branchConflict` state, which
      //      then pushes the `issue-detail-confirm` modal frame.
      // Once the fixture exists, replace the throw with the open
      // sequence. Keep the dispatch-isolation assertion below.
      throw new Error("issue-detail-confirm modal fixture not implemented");
    },
    todo: "Mock workspace POST 409 with git_head_ref conflict on an issue detail",
  },
  {
    name: "shortcut-help",
    open: async (page) => {
      // ReviewsView listens for "?" globally. The keyboard handler
      // is attached on mount, so we just need the route to render.
      await page.goto("/reviews");
      // Wait for the reviews shell to mount before pressing "?".
      await expect(page.locator(".reviews-view")).toBeVisible();
      await page.keyboard.press("Shift+/");
      // The modal title text comes from ShortcutHelpModal.
      await expect(
        page.getByRole("dialog", {
          name: /keyboard shortcuts/i,
        }),
      ).toBeVisible();
    },
  },
];

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

for (const m of MODAL_OPENERS) {
  test(`${m.name} modal blocks background j/k`, async ({ page }) => {
    if (m.todo) {
      test.fixme(true, `TODO: ${m.todo}`);
    }
    await m.open(page);

    // Snapshot list-selection state before any keypress.
    const beforeSelected = await page.locator(".pr-list-row.selected").count();

    // Background shortcuts that the modal frame must mask. Each is
    // sent through page.keyboard so it dispatches at the document
    // root — exactly where the global handler listens. If the modal
    // frame is missing, `j` would scroll selection forward and the
    // selection count would change.
    await page.keyboard.press("j");
    await page.keyboard.press("k");
    await page.keyboard.press("1");
    await page.keyboard.press("2");

    const afterSelected = await page.locator(".pr-list-row.selected").count();
    expect(afterSelected).toBe(beforeSelected);
  });
}
