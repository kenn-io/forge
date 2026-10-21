import { getFlash, showFlash } from "../stores/flash.svelte.js";

const launchErrors = new WeakSet<object>();

export function showWorkspaceLaunchError(message: string, tone: "danger" | "warning" | "success" = "danger"): void {
  showFlash(message, { tone });
  launchErrors.add(getFlash()!);
}

export function isWorkspaceLaunchError(flash: object): boolean {
  return launchErrors.has(flash);
}
