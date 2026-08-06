import type { LaunchTarget } from "../../api/types.js";

export function isVisibleLaunchTarget(target: LaunchTarget): boolean {
  if (target.kind === "shell") return false;
  return target.available || target.source !== "config";
}
