// Phone list-to-detail round trips. A phone list that opens a detail route
// records where it was in history state, so the detail header's Back control
// can pop straight back to that list. The list also parks its scroll offset
// and loaded chunk size here, keyed by list identity, so the remount after
// Back lands on the same rows the reader left.

export type MobileListOrigin = "activity" | "pulls" | "issues";

const originKey = "kennForgeMobileListOrigin";
const backDepthKey = "kennForgeMobileListBackDepth";

// The detail header's Back returns to the list in one tap, however many
// entries the detail pushed on top of the list's (a tab switch, a stack
// member), so the state also counts how far back that list entry sits.
export function mobileListOriginState(origin: MobileListOrigin, backDepth = 1): Record<string, unknown> {
  return { [originKey]: origin, [backDepthKey]: backDepth };
}

export function readMobileListBackDepth(state: unknown): number {
  if (typeof state !== "object" || state === null) return 1;
  const depth: unknown = Reflect.get(state, backDepthKey);
  return typeof depth === "number" && depth >= 1 ? depth : 1;
}

export function readMobileListOrigin(state: unknown): MobileListOrigin | undefined {
  if (typeof state !== "object" || state === null) return undefined;
  const origin: unknown = Reflect.get(state, originKey);
  return origin === "activity" || origin === "pulls" || origin === "issues" ? origin : undefined;
}

// A detail route that navigates again (a tab switch, a stack member) must
// carry the origin onto the entry it creates, or Back from that entry loses
// the list that opened the item. A pushed entry sits one step further from
// the list; a replaced entry keeps its distance. Only these keys travel:
// other history state belongs to the entry that wrote it.
export function carryMobileListOrigin(state: unknown, step: "push" | "replace"): Record<string, unknown> | undefined {
  const origin = readMobileListOrigin(state);
  if (origin === undefined) return undefined;
  const depth = readMobileListBackDepth(state) + (step === "push" ? 1 : 0);
  return mobileListOriginState(origin, depth);
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
const returnKey = "kennForgeMobileListReturn";

// Parking happens right before the list pushes a detail entry, so the list
// also stamps its own history entry with its identity. Only a return to that
// entry (the detail header's Back or the browser's Back) carries the stamp;
// a fresh visit to the same list (the mode picker, a deep link) lands on a
// new entry without it.
export function rememberMobileListPosition(key: string, position: MobileListPosition): void {
  positions.set(key, position);
  const current: unknown = history.state;
  const carried = typeof current === "object" && current !== null ? current : {};
  history.replaceState({ ...carried, [returnKey]: key }, "");
}

// Reading a parked position consumes it either way: the offset is restored
// only when the list mounts back on the entry it stamped, and a fresh visit
// discards it so the list starts at the top like any other first visit.
export function takeMobileListPosition(key: string): MobileListPosition | undefined {
  const position = positions.get(key);
  positions.delete(key);
  const state: unknown = history.state;
  const stamped: unknown = typeof state === "object" && state !== null ? Reflect.get(state, returnKey) : undefined;
  return stamped === key ? position : undefined;
}

export function scrollViewportOf(root: HTMLElement | null | undefined): HTMLElement | null {
  return root?.querySelector<HTMLElement>(".kit-scrollbox__viewport") ?? null;
}
