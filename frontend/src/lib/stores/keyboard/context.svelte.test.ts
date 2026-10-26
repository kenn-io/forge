import { describe, expect, it } from "vite-plus/test";
import type { PullSelection } from "../pulls.svelte.js";

import { buildContext } from "./context.svelte.js";

const samplePR: PullSelection = {
  owner: "acme",
  name: "widgets",
  number: 7,
  provider: "github",
  platformHost: "github.com",
  repoPath: "acme/widgets",
};

describe("buildContext", () => {
  it("forwards the pulls store's selected PR when no PR is in the route", () => {
    // The router has no PR in the URL during this test, so context.selectedPR
    // must come from the pulls store. A future regression where the dispatcher
    // reads from the wrong source (or drops the selection entirely) shows up
    // as this value going null.
    const ctx = buildContext({
      pulls: { getSelectedPR: () => samplePR },
      issues: { getSelectedIssue: () => null },
    });
    expect(ctx.selectedPR).toBe(samplePR);
    expect(ctx.selectedIssue).toBeNull();
  });
});
