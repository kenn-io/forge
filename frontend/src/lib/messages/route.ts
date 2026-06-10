export type MessagesRoute = {
  mode: "messages";
  q: string | null;
  message: string | null;
  view?: "linked" | undefined;
};

export function parseMessagesRoute(search: string): MessagesRoute {
  const params = new URLSearchParams(search.startsWith("?") ? search.slice(1) : search);
  const view = params.get("view");
  return {
    mode: "messages",
    q: params.get("q"),
    message: params.get("message"),
    ...(view === "linked" ? { view: "linked" as const } : {}),
  };
}

export function messagesSearch(route: MessagesRoute): string {
  const params = new URLSearchParams();
  if (route.q) params.set("q", route.q);
  if (route.message) params.set("message", route.message);
  if (route.view === "linked") params.set("view", "linked");
  const qs = params.toString();
  return qs ? `?${qs}` : "";
}

export function messagesHref(route: MessagesRoute): string {
  const qs = messagesSearch(route);
  return qs ? `/messages${qs}` : "/messages";
}

export const defaultMessagesRoute: MessagesRoute = { mode: "messages", q: null, message: null };

/**
 * Parse a MessagesRoute.message URL param into a positive integer message id,
 * or null if absent / malformed. Plain decimal digits only - `Number("1e2")`
 * and `Number("0x2a")` both succeed but would map to a different id than the
 * backend's base-10 path parser, and whitespace-padded values silently round
 * to a different integer. Reject anything that doesn't match `/^\d+$/`.
 */
export function messageIdFromRoute(raw: string | null): number | null {
  if (!raw) return null;
  if (!/^\d+$/.test(raw)) return null;
  const parsed = Number(raw);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null;
}
