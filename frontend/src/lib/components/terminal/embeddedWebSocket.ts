export function embeddedWebSocketUrl(path: string): string | null {
  const raw = window.__KENN_EMBEDDED_WEBSOCKET_BASE_URL__?.trim();
  if (!raw) return null;

  try {
    const base = new URL(raw);
    if (base.protocol !== "ws:" && base.protocol !== "wss:") return null;
    const requested = new URL(path, "http://middleman.local");
    const basePath = base.pathname.replace(/\/$/, "");
    base.pathname = `${basePath}${requested.pathname}`;
    base.search = requested.search;
    base.hash = "";
    return base.toString();
  } catch {
    return null;
  }
}
