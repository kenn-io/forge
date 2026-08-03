# Force-Push Lineage Restoration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan directly in the current agent, task-by-task. Never use subagent-driven development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore commits that become current again after a later force push, and prove rewind collapsing through the real SQLite/API/browser path.

**Approved spec/design:** `docs/superpowers/specs/2026-08-03-force-push-lineage-restoration-design.md`

**Architecture:** Replay force-push generations by event time while maintaining a set of obsolete stable commit orders. An `after_sha` already present in that set identifies a restored lineage: clear obsolete orders through that head before deciding whether the same push removes a different range. Keep the existing generation builder for missing-anchor behavior and use a dedicated SQLite seed plus Playwright assertion for the user-visible boundary.

**Tech Stack:** Svelte 5, TypeScript, Vite+/Vitest, Go, SQLite, Playwright.

## Global Constraints

- Preserve grouped timeline ordering; obsolete state affects collapsed runs only in strict date order.
- Use `commit_order_key` before `commit_order` or database IDs.
- Preserve existing missing-anchor force-push behavior.
- Do not add compatibility paths or general-purpose test helpers.
- Run the required Svelte analyzer on `EventTimeline.svelte`; if the pinned CLI remains unavailable, record that and use the repository Svelte/type checks as the verification fallback.
- Never use `--no-verify` for commits.
- Before each commit, run the repository `context-sync --commit` workflow and the mandatory commit skill.
- Before editing implementation files, record: "This is a `ui-only` plus `test-runtime` change; the likely regression surface is strict-date timeline rendering from SQLite/API-provided force-push metadata and stable commit order keys."

---

### Task 1: Replay Obsolete Lineage State

**Files:**
- Modify: `packages/ui/src/components/detail/EventTimeline.test.ts:1770`
- Modify: `packages/ui/src/components/detail/EventTimeline.svelte:605`

**Interfaces:**
- Consumes: `buildForcePushBoundaries(events): ForcePushBoundary[]`, `buildForcePushGenerations(boundaries): ForcePushGeneration[]`, and `commitOrder(event): number`.
- Produces: `obsoleteCommitOrders(events): Set<number>` used by `collapseObsoleteCommitEntries`.

- [ ] **Step 1: Add a failing component regression for lineage restoration**

Record the small-change classification sentence from Global Constraints, then insert these focused tests after the existing rewind-collapse case:

```ts
it("restores rewound commits when a later force push restores that lineage", () => {
  const commit1 = "1111111111111111111111111111111111111111";
  const commit2 = "2222222222222222222222222222222222222222";
  const commit3 = "3333333333333333333333333333333333333333";
  const { container } = render(EventTimeline, {
    props: {
      events: [
        makeEvent({
          ID: 5,
          EventType: "force_push",
          Summary: "1111111 -> 3333333",
          CreatedAt: "2024-06-01T13:00:00Z",
          MetadataJSON: JSON.stringify({ before_sha: commit1, after_sha: commit3 }),
        }),
        makeEvent({
          ID: 4,
          EventType: "force_push",
          Summary: "3333333 -> 1111111",
          CreatedAt: "2024-06-01T12:00:00Z",
          MetadataJSON: JSON.stringify({ before_sha: commit3, after_sha: commit1 }),
        }),
        makeEvent({
          ID: 3,
          EventType: "commit",
          Summary: commit3,
          Body: "commit three restored",
          CreatedAt: "2024-06-01T10:03:00Z",
          MetadataJSON: JSON.stringify({ commit_order: 3, commit_order_key: 3 }),
        }),
        makeEvent({
          ID: 2,
          EventType: "commit",
          Summary: commit2,
          Body: "commit two restored",
          CreatedAt: "2024-06-01T10:02:00Z",
          MetadataJSON: JSON.stringify({ commit_order: 2, commit_order_key: 2 }),
        }),
        makeEvent({
          ID: 1,
          EventType: "commit",
          Summary: commit1,
          Body: "commit one remains current",
          CreatedAt: "2024-06-01T10:01:00Z",
          MetadataJSON: JSON.stringify({ commit_order: 1, commit_order_key: 1 }),
        }),
      ],
      timelineOrder: "chronological",
    },
  });

  const text = container.textContent ?? "";
  expect(text).not.toContain("commits replaced by a later force push");
  expect(text).toContain("commit three restored");
  expect(text).toContain("commit two restored");
  expect(text).toContain("commit one remains current");
});
```

```ts
it("preserves unrelated obsolete commits when a later rewind targets current lineage", () => {
  const commits = [1, 2, 3, 4, 5].map((order) => `${order}`.repeat(40));
  const { container } = render(EventTimeline, {
    props: {
      events: [
        makeEvent({
          ID: 7,
          EventType: "force_push",
          Summary: "5555555 -> 3333333",
          CreatedAt: "2024-06-01T13:00:00Z",
          MetadataJSON: JSON.stringify({ before_sha: commits[4], after_sha: commits[2] }),
        }),
        makeEvent({
          ID: 6,
          EventType: "force_push",
          Summary: "2222222 -> 4444444",
          CreatedAt: "2024-06-01T12:00:00Z",
          MetadataJSON: JSON.stringify({ before_sha: commits[1], after_sha: commits[3] }),
        }),
        ...commits.map((summary, index) =>
          makeEvent({
            ID: index + 1,
            EventType: "commit",
            Summary: summary,
            Body: `lineage commit ${index + 1}`,
            CreatedAt: `2024-06-01T10:0${index + 1}:00Z`,
            MetadataJSON: JSON.stringify({ commit_order: index + 1, commit_order_key: index + 1 }),
          }),
        ),
      ],
      timelineOrder: "chronological",
    },
  });

  const text = container.textContent ?? "";
  expect(text).not.toContain("lineage commit 1");
  expect(text).not.toContain("lineage commit 2");
  expect(text).toContain("lineage commit 3");
  expect(text).not.toContain("lineage commit 4");
  expect(text).not.toContain("lineage commit 5");
});
```

The first test catches permanent range unioning. The second catches an over-broad restoration step that revives orders `1` and `2` when a later rewind `5 -> 3` should only remove orders `4` and `5`.

- [ ] **Step 2: Run the focused test and verify the red state**

Run:

```bash
(cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/detail/EventTimeline.test.ts -t "restores rewound commits|preserves unrelated obsolete commits")
```

Expected: FAIL: permanent range unioning fails the restoration case. Confirm the unrelated-lineage case does not pass because of an invalid fixture or runtime error.

- [ ] **Step 3: Replace permanent obsolete ranges with event-ordered state replay**

Replace `ObsoleteCommitRange`, `obsoleteCommitRanges`, and `isObsoleteCommitOrder` with:

```ts
function obsoleteCommitOrders(orderingSourceEvents: Array<PREvent | IssueEvent>): Set<number> {
  const commitOrders = orderingSourceEvents
    .filter((event) => event.EventType === "commit")
    .map(commitOrder);
  const generations = buildForcePushGenerations(buildForcePushBoundaries(orderingSourceEvents)).sort(
    (a, b) => a.pushedAt - b.pushedAt || a.eventID - b.eventID,
  );
  const obsolete = new Set<number>();
  const addThrough = (endAt: number, startAfter = 0): void => {
    for (const order of commitOrders) {
      if (order > startAfter && order <= endAt) obsolete.add(order);
    }
  };
  const restoreBetween = (startAt: number, endAt: number): void => {
    for (const order of commitOrders) {
      if (order >= startAt && order <= endAt) obsolete.delete(order);
    }
  };

  for (const generation of generations) {
    const before = generation.beforeCommitID;
    const after = generation.afterCommitID;
    const restoresObsoleteLineage = after !== undefined && obsolete.has(after);

    if (before !== undefined) {
      if (restoresObsoleteLineage && after !== undefined) {
        restoreBetween(Math.min(before, after), Math.max(before, after));
      }
      if (after !== undefined && after < before) {
        addThrough(before, after);
      } else if (!restoresObsoleteLineage) {
        addThrough(before);
      }
      continue;
    }
    if (restoresObsoleteLineage && after !== undefined) {
      obsolete.delete(after);
      continue;
    }
    if (!restoresObsoleteLineage && generation.effectiveStartAfterCommitID > 0) {
      addThrough(generation.effectiveStartAfterCommitID);
    }
  }
  return obsolete;
}
```

Update `collapseObsoleteCommitEntries` to compute `const obsoleteOrders = obsoleteCommitOrders(orderingSourceEvents)`, return early when its size is zero, and check `obsoleteOrders.has(commitOrder(entry.event))`.

- [ ] **Step 4: Run focused and neighboring component tests**

Run:

```bash
(cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/detail/EventTimeline.test.ts)
```

Expected: PASS, including the original rewind, same-length rebase, missing-anchor, compact-view, and restoration cases.

- [ ] **Step 5: Run Svelte analysis and package checks**

Run:

```bash
vp exec -- svelte-mcp svelte-autofixer packages/ui/src/components/detail/EventTimeline.svelte --svelte-version 5
node node_modules/vite-plus/bin/vp run ui-package-check
```

Expected: analyzer reports no actionable Svelte issue and `ui-package-check` passes. If `svelte-mcp` is still missing, preserve its exact error in the verification report and rely on the passing package check.

- [ ] **Step 6: Context-check and commit the lineage fix**

Run the `context-sync --commit` workflow, load the mandatory commit skill, then:

```bash
git add packages/ui/src/components/detail/EventTimeline.svelte packages/ui/src/components/detail/EventTimeline.test.ts
git commit -m "fix: restore force-pushed commit lineages" -m "Obsolete commit state must follow force-push event order so a restored head can reactivate only its own lineage without reviving unrelated replaced commits."
```

Expected: one verified commit containing only the component logic and regression test.

---

### Task 2: Prove Rewind Collapse Through SQLite and Playwright

**Files:**
- Modify: `frontend/tests/e2e-full/pr-timeline-filters.spec.ts:85`
- Modify: `internal/testutil/fixtures.go:280`
- Modify: `internal/testutil/fixtures.go:748`

**Interfaces:**
- Consumes: widgets PR #6 from `SeedFixtures`, the real `/api/v1/pulls/github/acme/widgets/6` response, and `openActivityViewMenu(page)`.
- Produces: a stable backend fixture with commit order keys `1`, `2`, and `3`, plus a browser regression selecting strict date order.

- [ ] **Step 1: Add the failing full-stack browser assertion**

Add this test after the existing force-push generation tests:

```ts
test("collapses only rewound commits from the SQLite-backed timeline", async ({ page }) => {
  await openPRTimelinePath(page, "/pulls/github/acme/widgets/6");
  const menu = await openActivityViewMenu(page);
  await menu.getByRole("button", { name: "Strict date order" }).click();

  await expect(page.getByText("2 commits replaced by a later force push", { exact: true })).toBeVisible();
  await expect(page.getByText("dashboard base remains current", { exact: true })).toBeVisible();
  await expect(page.getByText("dashboard filters rewound away", { exact: true })).toHaveCount(0);
  await expect(page.getByText("dashboard widgets rewound away", { exact: true })).toHaveCount(0);
});
```

The production mutation this catches is dropping rewind metadata or stable order keys anywhere between SQLite, the detail API, and strict-date rendering.

- [ ] **Step 2: Run the targeted Playwright test and verify the red state**

Run:

```bash
(cd frontend && node ../node_modules/vite-plus/bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/pr-timeline-filters.spec.ts --project=chromium --grep "SQLite-backed timeline")
```

Expected: FAIL because widgets PR #6 does not yet contain the named commit or force-push events.

- [ ] **Step 3: Seed a dedicated rewind timeline with stable order keys**

Capture the PR identifier by changing the widgets #6 insert from `_, err = d.UpsertMergeRequest` to `w6ID, err := d.UpsertMergeRequest`.

After the widgets PR #2 event block, insert three commits and one force push for `w6ID`:

```go
w6Commit1 := "6666111111111111111111111111111111111111"
w6Commit2 := "6666222222222222222222222222222222222222"
w6Commit3 := "6666333333333333333333333333333333333333"
err = d.UpsertMREvents(ctx, []db.MREvent{
  {
    MergeRequestID: w6ID,
    EventType:      "commit",
    Author:         "carol",
    Summary:        w6Commit1,
    Body:           "dashboard base remains current",
    MetadataJSON:   `{"commit_order":1,"commit_order_key":1}`,
    CreatedAt:      w6Created.Add(time.Hour),
    DedupeKey:      "w6-commit-1",
  },
  {
    MergeRequestID: w6ID,
    EventType:      "commit",
    Author:         "carol",
    Summary:        w6Commit2,
    Body:           "dashboard filters rewound away",
    MetadataJSON:   `{"commit_order":2,"commit_order_key":2}`,
    CreatedAt:      w6Created.Add(2 * time.Hour),
    DedupeKey:      "w6-commit-2",
  },
  {
    MergeRequestID: w6ID,
    EventType:      "commit",
    Author:         "carol",
    Summary:        w6Commit3,
    Body:           "dashboard widgets rewound away",
    MetadataJSON:   `{"commit_order":3,"commit_order_key":3}`,
    CreatedAt:      w6Created.Add(3 * time.Hour),
    DedupeKey:      "w6-commit-3",
  },
  {
    MergeRequestID: w6ID,
    EventType:      "force_push",
    Author:         "carol",
    Summary:        "6666333 -> 6666111",
    MetadataJSON:   fmt.Sprintf(`{"before_sha":%q,"after_sha":%q,"ref":"wip/dashboard"}`, w6Commit3, w6Commit1),
    CreatedAt:      w6Created.Add(4 * time.Hour),
    DedupeKey:      "w6-force-push-1",
  },
})
if err != nil {
  return nil, fmt.Errorf("upsert widgets PR#6 events: %w", err)
}
```

Use the repository formatter after applying the change; do not manually preserve spacing that `gofmt` changes.

- [ ] **Step 4: Verify the SQLite fixture and detail API remain valid**

Run:

```bash
go test ./internal/testutil ./internal/server/e2etest -run 'TestSeedFixtures|TestE2E_DetailTimeline' -shuffle=on
```

Expected: PASS with the existing fixture counts unchanged and timeline metadata still round-tripping through SQLite and HTTP.

- [ ] **Step 5: Re-run the targeted Playwright test and verify green**

Run:

```bash
(cd frontend && node ../node_modules/vite-plus/bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/pr-timeline-filters.spec.ts --project=chromium --grep "SQLite-backed timeline")
```

Expected: PASS against the real seeded Go server and SQLite database.

- [ ] **Step 6: Run the complete affected full-stack spec**

Run:

```bash
(cd frontend && node ../node_modules/vite-plus/bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/pr-timeline-filters.spec.ts --project=chromium)
```

Expected: PASS; existing grouped force-push ordering and reply-regroup cases remain intact.

- [ ] **Step 7: Context-check and commit the backend-integrated regression**

Run the `context-sync --commit` workflow, load the mandatory commit skill, then:

```bash
git add internal/testutil/fixtures.go frontend/tests/e2e-full/pr-timeline-filters.spec.ts
git commit -m "test: cover rewind collapse through SQLite" -m "The component regression cannot prove that stable commit order and rewind SHA metadata survive SQLite and the detail API before strict-date rendering."
```

Expected: one verified commit containing only the seed fixture and full-stack regression.

---

### Task 3: Final Small-Change Verification

**Files:**
- Verify only; modify files only if a formatter reports a scoped correction.

**Interfaces:**
- Consumes: the commits produced by Tasks 1 and 2.
- Produces: evidence that the component, package, Go fixture/API, and browser seams pass together.

- [ ] **Step 1: Run focused UI and Go verification together**

Run:

```bash
(cd frontend && node ../node_modules/vite-plus/bin/vp test run --project unit ../packages/ui/src/components/detail/EventTimeline.test.ts)
node node_modules/vite-plus/bin/vp run ui-package-check
go test ./internal/testutil ./internal/server/e2etest -run 'TestSeedFixtures|TestE2E_DetailTimeline' -shuffle=on
```

Expected: all commands pass without warnings attributable to the change.

- [ ] **Step 2: Run the affected Chromium full-stack spec**

Run:

```bash
(cd frontend && node ../node_modules/vite-plus/bin/vp exec -- playwright test --config=playwright-e2e.config.ts tests/e2e-full/pr-timeline-filters.spec.ts --project=chromium)
```

Expected: PASS.

- [ ] **Step 3: Review the final branch diff and status**

Run:

```bash
git status --short
git diff --stat HEAD~2..HEAD
git diff HEAD~2..HEAD -- packages/ui/src/components/detail/EventTimeline.svelte packages/ui/src/components/detail/EventTimeline.test.ts internal/testutil/fixtures.go frontend/tests/e2e-full/pr-timeline-filters.spec.ts
git diff
git diff --cached
```

Expected: the last two commits contain only the approved lineage fix, its component regression, the dedicated PR #6 fixture, and its full-stack assertion; `git status`, unstaged diff, and staged diff are clean.
