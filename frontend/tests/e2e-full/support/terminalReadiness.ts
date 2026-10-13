import { expect, type Locator, type Page } from "@playwright/test";

type TerminalSocketState = { pathname: string; inputReady: boolean; closed: boolean };
const terminalSockets = new WeakMap<Page, Map<object, TerminalSocketState>>();

export function trackTerminalReadiness(page: Page): void {
  const sockets = new Map<object, TerminalSocketState>();
  terminalSockets.set(page, sockets);
  page.on("websocket", (socket) => {
    const pathname = new URL(socket.url()).pathname;
    if (!pathname.includes("/runtime/sessions/") || !pathname.endsWith("/terminal")) return;
    const state = { pathname, inputReady: false, closed: false };
    sockets.set(socket, state);
    socket.on("framesent", ({ payload }) => {
      if (typeof payload !== "string") return;
      try {
        if ((JSON.parse(payload) as { type?: string }).type === "claim_resize") state.inputReady = true;
      } catch {
        // The terminal sends ordinary text as well as JSON control frames.
      }
    });
    socket.on("close", () => {
      state.closed = true;
    });
  });
}

export async function waitForTerminalInputReady(container: Locator): Promise<void> {
  const sockets = terminalSockets.get(container.page());
  if (!sockets) throw new Error("Install trackTerminalReadiness before opening a terminal");
  const identity = await container.evaluate((element) =>
    element.closest("[data-session-host]")?.getAttribute("data-session-host"),
  );
  if (!identity) throw new Error("Terminal has no data-session-host identity");
  const [workspaceId, , sessionKey] = identity.split("/").map(decodeURIComponent);
  if (!workspaceId || !sessionKey) throw new Error(`Invalid terminal session identity: ${identity}`);
  const sessionPath = `/workspaces/${encodeURIComponent(workspaceId)}/runtime/sessions/${encodeURIComponent(sessionKey)}/terminal`;
  await expect
    .poll(
      () => {
        return [...sockets.values()].some(
          (socket) => socket.pathname.endsWith(sessionPath) && socket.inputReady && !socket.closed,
        );
      },
      { message: `Terminal ${identity} must claim resize before input`, timeout: 15_000 },
    )
    .toBe(true);
}
