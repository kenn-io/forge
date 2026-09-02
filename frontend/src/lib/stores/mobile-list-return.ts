// Phone list-to-detail round trips. A phone list that opens a detail route
// records where it was in history state, so the detail header's Back control
// can pop straight back to that list. The list also parks its scroll offset
// and loaded chunk size here, keyed by list identity, so the remount after
// Back lands on the same rows the reader left.

export type MobileListOrigin = "activity" | "pulls" | "issues";

const originKey = "kennForgeMobileListOrigin";

export function mobileListOriginState(origin: MobileListOrigin): Record<string, unknown> {
  return { [originKey]: origin };
}

export function readMobileListOrigin(state: unknown): MobileListOrigin | undefined {
  if (typeof state !== "object" || state === null) return undefined;
  const origin: unknown = Reflect.get(state, originKey);
  return origin === "activity" || origin === "pulls" || origin === "issues" ? origin : undefined;
}

export function mobileListRoute(origin: MobileListOrigin): string {
  if (origin === "pulls") return "/m/pulls";
  if (origin === "issues") return "/m/issues";
  return "/m";
}

export function mobileListBackLabel(origin: MobileListOrigin): string {
  if (origin === "pulls") return "Pull requests";
  if (origin === "issues") return "Issues";
  return "Activity";
}

export interface MobileListPosition {
  scrollTop: number;
  pageLimit: number;
}

const positions = new Map<string, MobileListPosition>();

export function rememberMobileListPosition(key: string, position: MobileListPosition): void {
  positions.set(key, position);
}

// Reading a parked position consumes it: a later fresh visit to the same
// list (for example from the mode picker) starts at the top like any other
// first visit, and only the Back round trip restores the offset.
export function takeMobileListPosition(key: string): MobileListPosition | undefined {
  const position = positions.get(key);
  positions.delete(key);
  return position;
}

export function scrollViewportOf(root: HTMLElement | null | undefined): HTMLElement | null {
  return root?.querySelector<HTMLElement>(".kit-scrollbox__viewport") ?? null;
}
