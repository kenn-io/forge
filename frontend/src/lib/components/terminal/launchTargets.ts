import type { LaunchTarget } from "@middleman/ui/api/types";

export function isVisibleLaunchTarget(target: LaunchTarget): boolean {
  if (target.kind === "shell") return false;
  return target.available || target.source !== "config";
}
