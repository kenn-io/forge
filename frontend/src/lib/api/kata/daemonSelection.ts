import type { KataDaemonInfo } from "./daemons";

export function kataLinkingEnabledForEffectiveDaemon(
  daemons: readonly KataDaemonInfo[],
  activeId: string | undefined,
  defaultId: string | undefined,
): boolean {
  const effectiveId = activeId ?? defaultId;
  return daemons.some((daemon) => daemon.id === effectiveId && daemon.health === "connected");
}
