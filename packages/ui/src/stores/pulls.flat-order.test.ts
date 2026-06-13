// Store-level conversion of the grouping-toggle e2e case
// "j/k navigation follows flat order in ungrouped mode"
// (frontend/tests/e2e-full/grouping-toggle.spec.ts). The app's "j"/"k"
// bindings call pulls.selectNextPR()/selectPrevPR() (see
// frontend/src/lib/stores/keyboard/actions.ts), and the traversal order is
// computed here in the pulls store via getDisplayOrderPRs() with the real
// grouping store wired in — so this is the layer that owns the behavior.
import { beforeAll, beforeEach, describe, expect, it } from "vite-plus/test";

import type { PullRequest } from "../api/types.js";
import { seedGet } from "../testing/seedClient.js";
import type { MiddlemanClient } from "../types.js";
import { createGroupingStore } from "./grouping.svelte.js";
import { createPullsStore } from "./pulls.svelte.js";

// Seed data (captured from the real seeded e2e Go server). Flat (fixture)
// order interleaves repos by recency; grouped order clusters by repo.
let pullsFixture: PullRequest[];

beforeAll(async () => {
  pullsFixture = await seedGet<PullRequest[]>("/api/v1/pulls");
});

const flatOrder = [
  "acme/widgets#1",
  "acme/widgets#7",
  "acme/widgets#6",
  "acme/tools#1",
  "acme/widgets#2",
  "acme/tools#12",
  "acme/tools#11",
  "acme/tools#10",
];

const groupedOrder = [
  "acme/widgets#1",
  "acme/widgets#7",
  "acme/widgets#6",
  "acme/widgets#2",
  "acme/tools#1",
  "acme/tools#12",
  "acme/tools#11",
  "acme/tools#10",
];

function fakeClient(): MiddlemanClient {
  return {
    GET: async (path: string) => {
      if (path === "/pulls") {
        return { data: structuredClone(pullsFixture) };
      }
      return { error: { title: "unexpected request", detail: path } };
    },
  } as unknown as MiddlemanClient;
}

async function loadedStore() {
  const grouping = createGroupingStore();
  const pulls = createPullsStore({
    client: fakeClient(),
    getGroupByRepo: () => grouping.getGroupByRepo(),
  });
  await pulls.loadPulls();
  expect(pulls.getPulls()).toHaveLength(8);
  return { pulls, grouping };
}

function selectionKey(pulls: ReturnType<typeof createPullsStore>): string {
  const sel = pulls.getSelectedPR();
  expect(sel).not.toBeNull();
  return `${sel!.owner}/${sel!.name}#${sel!.number}`;
}

function traverseAll(pulls: ReturnType<typeof createPullsStore>): string[] {
  const visited: string[] = [];
  for (let i = 0; i < 8; i++) {
    pulls.selectNextPR();
    visited.push(selectionKey(pulls));
  }
  return visited;
}

describe("pulls store next/prev selection order", () => {
  beforeEach(() => {
    localStorage.removeItem("middleman:groupingMode");
    localStorage.removeItem("middleman:groupByRepo");
    localStorage.removeItem("middleman:hideOrgName");
  });

  it("follows flat display order in ungrouped mode", async () => {
    const { pulls, grouping } = await loadedStore();
    grouping.setGroupingMode("flat");

    // Mirror of the e2e assertion: two "j" presses land on the first two
    // visible flat items.
    pulls.selectNextPR();
    expect(selectionKey(pulls)).toBe(flatOrder[0]);
    pulls.selectNextPR();
    expect(selectionKey(pulls)).toBe(flatOrder[1]);

    // "k" walks back up the same flat order.
    pulls.selectPrevPR();
    expect(selectionKey(pulls)).toBe(flatOrder[0]);

    // Full traversal matches getPulls() flat display order exactly.
    pulls.clearSelection();
    expect(traverseAll(pulls)).toEqual(flatOrder);
    expect(flatOrder).toEqual(pullsFixture.map((pr) => `${pr.repo_owner}/${pr.repo_name}#${pr.Number}`));
  });

  it("follows repo-grouped order in grouped mode, proving order tracks the mode", async () => {
    const { pulls, grouping } = await loadedStore();
    grouping.setGroupingMode("byRepo");

    expect(traverseAll(pulls)).toEqual(groupedOrder);
    // The two modes genuinely diverge (4th item: widgets#2 vs tools#1), so
    // the flat-order assertion above is not vacuous.
    expect(groupedOrder).not.toEqual(flatOrder);
  });
});
