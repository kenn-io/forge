import { describe, expect, it } from "vite-plus/test";
import { mergeActionState, mergeRules, type MergeActionState, type MergeCI, type MergeRules } from "./merge-policy.js";

const RULES_BLOCK = "Blocked by branch protection or repository rules";
const NEEDS_BYPASS = "Needs a bypass of branch protection or repository rules; merge it from the pull request page";

const allowed: MergeActionState = {
  mergeAfterCI: false,
  immediate: "allowed",
  disabledReason: undefined,
  menuDisabledReason: undefined,
};
const needsBypass: MergeActionState = {
  mergeAfterCI: false,
  immediate: "needs_bypass",
  disabledReason: undefined,
  menuDisabledReason: NEEDS_BYPASS,
};
const unavailable: MergeActionState = {
  mergeAfterCI: false,
  immediate: "unavailable",
  disabledReason: RULES_BLOCK,
  menuDisabledReason: RULES_BLOCK,
};
const pending = (immediate: MergeActionState["immediate"]): MergeActionState => ({
  mergeAfterCI: true,
  immediate,
  disabledReason: undefined,
  menuDisabledReason: undefined,
});

// Every input combination of the merge window table in context/deferred-merge.md.
const table: Array<[MergeCI, MergeRules, boolean, MergeActionState]> = [
  ["pending", "clear", false, pending("allowed")],
  ["pending", "blocked_bypassable", false, pending("needs_bypass")],
  ["pending", "blocked", false, pending("unavailable")],
  ["passed", "clear", false, allowed],
  ["passed", "blocked_bypassable", false, needsBypass],
  ["passed", "blocked", false, unavailable],
  // Failed CI keeps its own handling; the rules still decide the immediate merge.
  ["failed", "clear", false, allowed],
  ["failed", "blocked_bypassable", false, needsBypass],
  ["failed", "blocked", false, unavailable],
  // A merge queue target is exempt from the rules.
  ["pending", "clear", true, pending("allowed")],
  ["pending", "blocked_bypassable", true, pending("allowed")],
  ["pending", "blocked", true, pending("allowed")],
  ["passed", "clear", true, allowed],
  ["passed", "blocked_bypassable", true, allowed],
  ["passed", "blocked", true, allowed],
  ["failed", "clear", true, allowed],
  ["failed", "blocked_bypassable", true, allowed],
  ["failed", "blocked", true, allowed],
];

describe("mergeActionState", () => {
  it.each(table)("ci %s, rules %s, queue target %s", (ci, rules, queueTarget, expected) => {
    expect(mergeActionState(ci, rules, queueTarget)).toEqual(expected);
  });
});

describe("mergeRules", () => {
  it("uses the merge credential's view over the synced state", () => {
    for (const capable of [true, false]) {
      expect(mergeRules({ State: "open", MergeableState: "blocked", merge_rules: "clear" }, capable)).toBe("clear");
      expect(mergeRules({ State: "open", MergeableState: "blocked", merge_rules: "blocked_bypassable" }, capable)).toBe(
        "blocked_bypassable",
      );
      expect(mergeRules({ State: "open", MergeableState: "blocked", merge_rules: "blocked" }, capable)).toBe("blocked");
    }
  });

  it("ignores rules read while the synced state was blocked once it no longer is", () => {
    // Read while CI was pending; a later sync saw the pull request clean.
    expect(mergeRules({ State: "open", MergeableState: "clean", merge_rules: "blocked" }, true)).toBe("clear");
    expect(mergeRules({ State: "open", MergeableState: "clean", merge_rules: "blocked_bypassable" }, true)).toBe(
      "clear",
    );
  });

  it("offers the merge without a bypass while the merge credential's rules are unknown", () => {
    // The sync credential's blocked state does not speak for the merge credential.
    expect(mergeRules({ State: "open", MergeableState: "blocked", merge_rules: "" }, true)).toBe("clear");
    expect(mergeRules({ State: "open", MergeableState: "clean", merge_rules: "" }, true)).toBe("clear");
  });

  it("keeps the synced blocked state on providers without merge credential rules", () => {
    // GitLab's blocked state is the same for every caller and has no bypass.
    expect(mergeRules({ State: "open", MergeableState: "blocked", merge_rules: "" }, false)).toBe("blocked");
    expect(mergeRules({ State: "open", MergeableState: "clean", merge_rules: "" }, false)).toBe("clear");
  });
});
