import type { DiffScope } from "../../stores/diff.svelte.js";

export function formatDiffScopeLabel(scope: DiffScope): string {
  if (scope.kind === "head") return "HEAD";
  if (scope.kind === "commit") return scope.sha.slice(0, 7);
  return `${scope.fromSha.slice(0, 7)}..${scope.toSha.slice(0, 7)}`;
}
