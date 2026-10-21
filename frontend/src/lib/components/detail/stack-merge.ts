import type { StackContextResponse } from "../../api/generated/models/stackContextResponse.js";

/**
 * Which pull requests a merge lands when the provider owns the stack.
 * Merging a native stack member also merges every unmerged member below it,
 * and the open members above it stay open on the new base.
 */
export interface NativeStackMergeScope {
  baseBranch: string;
  /** Bottom-to-top numbers that merge, ending with the merged pull request. */
  landing: number[];
  /** Open members above the merged pull request. */
  remaining: number[];
}

export function nativeStackMergeScope(
  stack: StackContextResponse | undefined,
  number: number,
): NativeStackMergeScope | undefined {
  if (!stack?.native) return undefined;
  const members = [...stack.members].sort((a, b) => a.position - b.position);
  const self = members.find((member) => member.number === number);
  if (!self) return undefined;
  return {
    baseBranch: members[0]?.base_branch ?? "",
    landing: members
      .filter((member) => member.position <= self.position && member.state !== "merged")
      .map((member) => member.number),
    remaining: members
      .filter((member) => member.position > self.position && member.state === "open")
      .map((member) => member.number),
  };
}
