import { expect, type Locator, type Page } from "@playwright/test";

type TerminalSocketState = { ready: boolean; closed: boolean };
const terminalSockets = new WeakMap<Page, Map<string, TerminalSocketState>>();

export function trackTerminalReadiness(page: Page): void {
  if (terminalSockets.has(page)) return;
  const sockets = new Map<string, TerminalSocketState>();
  terminalSockets.set(page, sockets);
  page.on("websocket", (socket) => {
    const pathname = new URL(socket.url()).pathname;
    const runtimePath = pathname.match(/\/ws\/v1\/.*\/runtime\/sessions\/[^/]+\/terminal$/)?.[0];
    if (!runtimePath) return;
    const state = { ready: false, closed: false };
    sockets.set(runtimePath, state);
    socket.on("framereceived", ({ payload }) => {
      if (typeof payload !== "string") return;
      try {
        if (JSON.parse(payload).type === "replay_ready") state.ready = true;
      } catch {
        // Terminal output frames can contain raw text rather than control JSON.
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
  const [workspaceId, hostKey, sessionKey] = identity.split("/").map(decodeURIComponent);
  if (!workspaceId || !sessionKey) throw new Error(`Invalid terminal session identity: ${identity}`);
  const hostPrefix = hostKey?.startsWith("devbox:")
    ? `/devboxes/${encodeURIComponent(hostKey.slice(7))}`
    : hostKey
      ? `/fleet/hosts/${encodeURIComponent(hostKey)}`
      : "";
  const runtimePath = `/ws/v1${hostPrefix}/workspaces/${encodeURIComponent(workspaceId)}/runtime/sessions/${encodeURIComponent(sessionKey)}/terminal`;
  await expect
    .poll(
      () => {
        const socket = sockets.get(runtimePath);
        return socket?.ready === true && !socket.closed;
      },
      { message: `Terminal ${identity} must receive replay_ready before input`, timeout: 15_000 },
    )
    .toBe(true);
}
