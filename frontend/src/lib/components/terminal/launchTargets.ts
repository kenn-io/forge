import type { LaunchTarget } from "../../api/types.js";

function isVisibleLaunchTarget(target: LaunchTarget): boolean {
  if (target.kind === "shell") return false;
  return target.available || target.source !== "config";
}

function isAgentLaunchTarget(target: LaunchTarget): boolean {
  return target.kind === "agent" || target.kind === "acp";
}

/** Orders agents alphabetically by label regardless of whether they came from
 *  config or builtin detection, keeping system targets (shells) after them in
 *  their original order. */
export function sortLaunchTargets(targets: readonly LaunchTarget[]): LaunchTarget[] {
  const agents = targets
    .filter(isAgentLaunchTarget)
    .sort((a, b) => a.label.localeCompare(b.label, undefined, { sensitivity: "base", numeric: true }));
  return [...agents, ...targets.filter((target) => !isAgentLaunchTarget(target))];
}

export function visibleLaunchTargets(targets: readonly LaunchTarget[]): LaunchTarget[] {
  return sortLaunchTargets(targets.filter(isVisibleLaunchTarget));
}
