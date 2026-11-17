import { expect, type Locator, type Page } from "@playwright/test";

type TerminalSocketState = { pathname: string; inputReady: boolean; closed: boolean; input: string };
const terminalSockets = new WeakMap<Page, Map<object, TerminalSocketState>>();

export function trackTerminalReadiness(page: Page): void {
  const sockets = new Map<object, TerminalSocketState>();
  terminalSockets.set(page, sockets);
  page.on("websocket", (socket) => {
    const pathname = new URL(socket.url()).pathname;
    if (!pathname.includes("/runtime/sessions/") || !pathname.endsWith("/terminal")) return;
    const state = { pathname, inputReady: false, closed: false, input: "" };
    sockets.set(socket, state);
    const decoder = new TextDecoder();
    socket.on("framesent", ({ payload }) => {
      if (typeof payload !== "string") {
        // Terminal input is binary; text frames carry controls.
        state.input += decoder.decode(payload, { stream: true });
        return;
      }
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

function trackedTerminalSockets(page: Page): Map<object, TerminalSocketState> {
  const sockets = terminalSockets.get(page);
  if (!sockets) throw new Error("Install trackTerminalReadiness before opening a terminal");
  return sockets;
}

export async function waitForTerminalInputReady(container: Locator): Promise<void> {
  const sockets = trackedTerminalSockets(container.page());
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

/**
 * Inserts text into the focused terminal and waits until a terminal socket has sent it.
 *
 * Firefox's insertText commits an IME composition, and xterm.js sends a committed
 * composition from a zero-delay timer. A key pressed before that timer runs makes
 * xterm send an empty range instead, so the text never reaches the shell. Press
 * the next key only after this resolves.
 */
export async function insertTerminalText(page: Page, text: string): Promise<void> {
  const sockets = trackedTerminalSockets(page);
  const sentBefore = new Map([...sockets].map(([socket, state]) => [socket, state.input.length]));
  await page.keyboard.insertText(text);
  await expect
    .poll(() => [...sockets].some(([socket, state]) => state.input.slice(sentBefore.get(socket) ?? 0).includes(text)), {
      message: "Inserted terminal text must be sent before the next key",
    })
    .toBe(true);
}
