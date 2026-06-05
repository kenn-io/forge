import { expect, test } from "@playwright/test";

import { mockApi } from "./support/mockApi";

// One comprehensive tour that walks every major keybindings interaction in
// sequence so a viewer of the recorded webm can see the whole feature
// working: palette open/close, command-prefix dispatch, PR-prefix search,
// arrow-key highlight, click-to-navigate, recents persistence, reserved
// prefix placeholder, escape/close behavior, cheatsheet open/filter/close,
// sidebar toggle, and modal-isolation guarantees (no background dispatch,
// Cmd+P closes the palette instead of opening the browser print dialog).
//
// Video recording is opt-in for this file only so the existing e2e tests
// don't pay the capture overhead. The webm lands under test-results/.

test.use({
  video: { mode: "on", size: { width: 1280, height: 720 } },
  viewport: { width: 1280, height: 720 },
});

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("keybindings tour: palette, recents, reserved, cheatsheet, sidebar, modal isolation", async ({
  page,
}, testInfo) => {
  // Generous overall timeout: the tour is intentionally paced so each step
  // is visible in the recording.
  testInfo.setTimeout(90_000);

  await page.goto("/pulls");

  // Wait for the PR list to render so j/k navigation and the palette PR
  // group have data to work with.
  const firstPrTitle = page.getByText("Add browser regression coverage");
  await expect(firstPrTitle).toBeVisible({ timeout: 15_000 });

  // Ensure the page (not the URL bar) has keyboard focus before sending
  // global keyboard shortcuts. Clicking the main app body is the cheapest
  // way to focus the document. Without this, Cmd+K can race with browser
  // chrome and silently miss the listener.
  await page.locator("main.app-main").click();

  // ---- Step 1: open palette via Cmd+K -------------------------------------
  await page.keyboard.press("Meta+K");
  const palette = page.getByRole("dialog", { name: "Command palette" });
  await expect(palette).toBeVisible();
  await expect(page.locator(".palette-input")).toBeFocused();
  await page.waitForTimeout(400);

  // ---- Step 2: type a command-prefix query, see the Commands group --------
  await page.locator(".palette-input").fill(">settings");
  const commandsGroup = palette.locator(".palette-group", {
    hasText: "Commands",
  });
  await expect(commandsGroup).toBeVisible();
  await expect(commandsGroup).toContainText(/Settings/i);
  await page.waitForTimeout(400);

  // ---- Step 3: run command (Enter) navigates to /settings ----------------
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/settings/);
  await page.waitForTimeout(500);

  // Go back to /pulls for the rest of the tour.
  await page.goto("/pulls");
  await expect(firstPrTitle).toBeVisible({ timeout: 10_000 });

  // ---- Step 4: reopen palette and use the PR prefix ----------------------
  await page.keyboard.press("Meta+K");
  await expect(palette).toBeVisible();
  await page.locator(".palette-input").fill("pr:");
  const pullsGroup = palette.locator(".palette-group", {
    hasText: "Pull requests",
  });
  await expect(pullsGroup).toBeVisible();
  // mockApi seeds 3 PRs; at least 2 should render under the prefix scope.
  await expect(pullsGroup.locator(".palette-row")).toHaveCount(3);
  await page.waitForTimeout(400);

  // ---- Step 5: ArrowDown moves the highlight to the second row -----------
  // Before the press, the first row should be highlighted (index 0).
  const firstRow = pullsGroup.locator(".palette-row").nth(0);
  const secondRow = pullsGroup.locator(".palette-row").nth(1);
  await expect(firstRow).toHaveClass(/palette-row-highlight/);
  await page.keyboard.press("ArrowDown");
  await expect(secondRow).toHaveClass(/palette-row-highlight/);
  // Preview pane updates to reflect the second row.
  await expect(page.locator(".palette-preview")).toContainText(
    /Mirror host stub PR|Refactor theme system|Add browser regression/,
  );
  await page.waitForTimeout(500);

  // ---- Step 6: click a PR row -> /pulls/detail ---------------------------
  await pullsGroup.locator(".palette-row").nth(0).click();
  await expect(page).toHaveURL(/\/pulls\/detail/);
  await page.waitForTimeout(800);

  // ---- Step 7: recents — go back, reopen palette, see the chosen PR ------
  await page.goto("/pulls");
  await expect(firstPrTitle).toBeVisible({ timeout: 10_000 });
  await page.keyboard.press("Meta+K");
  await expect(palette).toBeVisible();
  const recents = palette.locator(".palette-group", {
    hasText: "Recently used",
  });
  await expect(recents).toBeVisible();
  await expect(recents.locator(".palette-row").first()).toContainText(/#/);
  await page.waitForTimeout(500);

  // ---- Step 8: reserved prefix shows the v2-search placeholder -----------
  await page.locator(".palette-input").fill("repo:foo");
  const reservedRow = palette.locator(".palette-row-disabled");
  await expect(reservedRow).toBeVisible();
  await expect(reservedRow).toContainText(/v2|pr:|issue:/);
  await page.waitForTimeout(500);

  // ---- Step 9: Escape closes the palette ---------------------------------
  await page.keyboard.press("Escape");
  await expect(palette).toBeHidden();
  await page.waitForTimeout(400);

  // ---- Step 10: open cheatsheet via ? -----------------------------------
  await page.keyboard.press("?");
  const cheatsheet = page.getByRole("dialog", {
    name: "Keyboard shortcuts",
  });
  await expect(cheatsheet).toBeVisible();
  await expect(cheatsheet).toContainText(/Next pull request/i);
  await page.waitForTimeout(500);

  // ---- Step 11: filter cheatsheet narrows the rendered set --------------
  const cheatsheetFilter = cheatsheet.locator(".cheatsheet-filter");
  await cheatsheetFilter.fill("next");
  // After filtering, "Next pull request" is still visible but unrelated
  // actions like "Toggle theme" should no longer be present.
  await expect(cheatsheet).toContainText(/Next pull request/i);
  await expect(cheatsheet.getByText(/Toggle theme/i)).toHaveCount(0);
  await page.waitForTimeout(500);

  // ---- Step 12: Escape closes the cheatsheet -----------------------------
  await page.keyboard.press("Escape");
  await expect(cheatsheet).toBeHidden();
  await page.waitForTimeout(400);

  // ---- Step 13: Cmd+[ toggles the sidebar -------------------------------
  // The sidebar lives under .sidebar; the collapsed variant adds
  // .sidebar--collapsed. Capture the initial collapsed-ness, toggle once,
  // and verify the state flipped.
  const sidebar = page.locator(".sidebar").first();
  const wasCollapsed = await sidebar.evaluate((el) => el.classList.contains("sidebar--collapsed")).catch(() => false);
  await page.keyboard.press("Meta+[");
  await page.waitForTimeout(400);
  // After toggle the sidebar element may have been swapped between the
  // expanded `aside.sidebar` and the collapsed `aside.sidebar.sidebar--collapsed`,
  // so re-query and re-check the class.
  const sidebarAfter = page.locator(".sidebar").first();
  const isCollapsedAfter = await sidebarAfter.evaluate((el) => el.classList.contains("sidebar--collapsed"));
  expect(isCollapsedAfter).toBe(!wasCollapsed);
  // Toggle back so the rest of the recording shows the expanded layout
  // (purely cosmetic — the assertion above is the actual proof).
  await page.keyboard.press("Meta+[");
  await page.waitForTimeout(400);

  // ---- Step 14: modal isolation — j inside palette stays as a literal ----
  await page.keyboard.press("Meta+K");
  await expect(palette).toBeVisible();
  // Snapshot the PR-list selection state before the j keypress. With the
  // palette modal frame active, `j` should land in the search input as a
  // literal character and must NOT change the list selection underneath.
  const beforeSelectedCount = await page.locator(".pull-item.selected").count();
  // Clear any prior input text first so the j keypress lands in an
  // empty field and the resulting value is unambiguous to assert.
  await page.locator(".palette-input").fill("");
  await page.locator(".palette-input").focus();
  await page.keyboard.press("j");
  await expect(page.locator(".palette-input")).toHaveValue("j");
  const afterSelectedCount = await page.locator(".pull-item.selected").count();
  expect(afterSelectedCount).toBe(beforeSelectedCount);
  await page.waitForTimeout(500);

  // ---- Step 15: Cmd+P inside the palette closes it (no print dialog) ----
  await page.keyboard.press("Meta+P");
  await expect(palette).toBeHidden();
  await page.waitForTimeout(800);
});

test.afterAll(async () => {
  // Verify that the video file actually landed under test-results/.
  // Playwright writes one webm per test attempt under a per-test subdir
  // when video is enabled; we just need at least one to be present.
  const fs = await import("node:fs");
  const path = await import("node:path");
  const root = path.resolve(process.cwd(), "test-results");
  function walk(dir: string): string[] {
    const out: string[] = [];
    let entries: import("node:fs").Dirent[];
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return out;
    }
    for (const e of entries) {
      const full = path.join(dir, e.name);
      if (e.isDirectory()) out.push(...walk(full));
      else if (e.isFile() && full.endsWith(".webm")) out.push(full);
    }
    return out;
  }
  const webms = walk(root).filter((p) => p.includes("keybindings-tour"));
  if (webms.length === 0) {
    throw new Error(`expected at least one webm under ${root}, found none`);
  }
  const sizes = webms.map((p) => ({ p, size: fs.statSync(p).size }));
  console.log(`keybindings-tour video files:\n${sizes.map((s) => `  ${s.p} (${s.size} bytes)`).join("\n")}`);
  // Sanity: at least one should be a few KB. Empty webms mean video
  // capture didn't actually record anything.
  const largest = sizes.reduce((acc, s) => (s.size > acc.size ? s : acc), sizes[0]!);
  if (largest.size < 1024) {
    throw new Error(`largest webm is ${largest.size} bytes (< 1024), video capture likely failed`);
  }
});
