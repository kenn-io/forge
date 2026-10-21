import { describe, expect, it } from "vite-plus/test";

import type { StackContextResponse } from "../../api/generated/models/stackContextResponse.js";
import { nativeStackMergeScope } from "./stack-merge.js";

function member(number: number, position: number, state = "open") {
  return {
    number,
    position,
    state,
    title: `PR ${number}`,
    base_branch: position === 1 ? "main" : `branch-${position - 1}`,
    blocked_by: null,
    ci_status: "success",
    is_draft: false,
    mergeable_state: "clean",
    review_decision: "",
  };
}

function stack(native: boolean, position: number, members: ReturnType<typeof member>[]): StackContextResponse {
  return {
    health: "ready",
    native,
    position,
    size: members.length,
    stack_id: 1,
    stack_name: "feature",
    members,
  };
}

describe("nativeStackMergeScope", () => {
  it("lands every unmerged member below the merged pull request and keeps the ones above open", () => {
    const scope = nativeStackMergeScope(
      stack(true, 3, [member(14, 4), member(11, 1, "merged"), member(12, 2), member(13, 3)]),
      13,
    );

    expect(scope).toEqual({ baseBranch: "main", landing: [12, 13], remaining: [14] });
  });

  it("leaves closed members above out of the pull requests that stay open", () => {
    const scope = nativeStackMergeScope(stack(true, 1, [member(11, 1), member(12, 2, "closed"), member(13, 3)]), 11);

    expect(scope).toEqual({ baseBranch: "main", landing: [11], remaining: [13] });
  });

  it("does not describe branch-inferred stacks as stack merges", () => {
    expect(nativeStackMergeScope(stack(false, 2, [member(11, 1), member(12, 2)]), 12)).toBeUndefined();
    expect(nativeStackMergeScope(undefined, 12)).toBeUndefined();
  });
});
