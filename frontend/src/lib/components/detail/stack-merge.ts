import type { StackContextResponse } from "../../api/generated/models/stackContextResponse.js";
import type { StackMemberResponse } from "../../api/generated/models/stackMemberResponse.js";

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

function byPosition(stack: StackContextResponse): StackMemberResponse[] {
  return [...stack.members].sort((a, b) => a.position - b.position);
}

export function nativeStackMergeScope(
  stack: StackContextResponse | undefined,
  number: number,
): NativeStackMergeScope | undefined {
  if (!stack?.native) return undefined;
  const members = byPosition(stack);
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

/**
 * The open members a native stack merge can stop at, bottom first. Empty when
 * the provider does not own the stack.
 */
export function nativeStackMergeTargets(stack: StackContextResponse | undefined): StackMemberResponse[] {
  if (!stack?.native) return [];
  return byPosition(stack).filter((member) => member.state === "open");
}

/** The unmerged members a merge of `number` lands with it, bottom first. */
export function nativeStackMembersBelow(
  stack: StackContextResponse | undefined,
  number: number,
): StackMemberResponse[] {
  const scope = nativeStackMergeScope(stack, number);
  if (!stack || !scope) return [];
  const below = new Set(scope.landing.filter((n) => n !== number));
  return byPosition(stack).filter((member) => below.has(member.number));
}
