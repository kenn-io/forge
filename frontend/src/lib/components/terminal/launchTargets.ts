import type { LaunchTarget } from "@kenn-forge/ui/api/types";

export function isVisibleLaunchTarget(target: LaunchTarget): boolean {
  if (target.kind === "shell") return false;
  return target.available || target.source !== "config";
}
