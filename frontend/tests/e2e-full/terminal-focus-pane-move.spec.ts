import { execFileSync } from "node:child_process";
import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
} from "@playwright/test";
import { startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

// Regression for the pane-move focus loss shipped with the rearrangeable
// detail panes: moving a pooled session terminal reparents its DOM subtree
// through a display:none parking node, silently blurring xterm's helper
// textarea. Keyboard input and keyboard-driven tmux copy/paste must keep
// working after the move WITHOUT clicking the terminal again — the existing
// clipboard spec clicks before every operation, which masks exactly this.

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  error_message?: string | null;
};

type WorkspaceRuntimeResponse = {
  sessions?: Array<{ key: string; status: string }>;
};

const preMarker = "TERM_FOCUS_PRE_MOVE";
const postMarker = "TERM_FOCUS_POST_MOVE";
const clipboardMarker = "TERM_FOCUS_TMUX_CLIP";

function hasCommand(command: string, args: string[] = ["--version"]): boolean {
  try {
    execFileSync(command, args, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

async function waitForWorkspaceReady(api: APIRequestContext, workspaceId: string): Promise<WorkspaceStatusResponse> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const response = await api.get(`/api/v1/workspaces/${workspaceId}`);
    expect(response.ok()).toBe(true);
    const workspace = (await response.json()) as WorkspaceStatusResponse;
    if (workspace.status === "ready") return workspace;
    if (workspace.status === "error") {
      throw new Error(workspace.error_message ?? `workspace ${workspaceId} failed to become ready`);
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`workspace ${workspaceId} did not become ready`);
}

async function createIssueWorkspace(api: APIRequestContext, issueNumber = 10): Promise<WorkspaceStatusResponse> {
  const response = await api.post(`/api/v1/issues/github/acme/widgets/${issueNumber}/workspace`, {
    data: {},
  });
  expect(response.status()).toBe(202);
  const workspace = (await response.json()) as WorkspaceStatusResponse;
  return waitForWorkspaceReady(api, workspace.id);
}

async function launchShell(
  api: APIRequestContext,
  workspaceId: string,
  region: "terminal" | "workflow",
): Promise<void> {
  const response = await api.post(`/api/v1/workspaces/${workspaceId}/runtime/sessions`, {
    data: {
      target_key: "plain_shell",
      display_region: region,
    },
  });
  expect(response.status(), await response.text()).toBe(200);
}

async function launchDockedShell(api: APIRequestContext, workspaceId: string): Promise<void> {
  await launchShell(api, workspaceId, "terminal");
}

async function runningSessionKeys(api: APIRequestContext, workspaceId: string): Promise<string[]> {
  const response = await api.get(`/api/v1/workspaces/${workspaceId}/runtime`);
  expect(response.ok()).toBe(true);
  const runtime = (await response.json()) as WorkspaceRuntimeResponse;
  return (runtime.sessions ?? []).filter((session) => session.status === "running").map((session) => session.key);
}

async function focusedSessionHost(page: Page): Promise<string | null> {
  return page.evaluate(
    () => document.activeElement?.closest("[data-session-host]")?.getAttribute("data-session-host") ?? null,
  );
}

async function openTerminalPanel(page: Page): Promise<Locator> {
  const open = page.getByRole("button", { name: "Open terminal panel" });
  await expect(open).toBeVisible({ timeout: 15_000 });
  await open.click();
  const panel = page.locator(".terminal-panel.open");
  await expect(panel).toBeVisible();
  return panel;
}

function observeTerminal(page: Page): {
  commandPromptOccurrences(): number;
  received(text: string): boolean;
  sent(text: string): boolean;
} {
  const streams: Array<{ received: string; sent: string }> = [];
  page.on("websocket", (socket) => {
    const stream = { received: "", sent: "" };
    streams.push(stream);
    const receivedDecoder = new TextDecoder();
    const sentDecoder = new TextDecoder();
    socket.on("framereceived", ({ payload }) => {
      const chunk = typeof payload === "string" ? payload : receivedDecoder.decode(payload, { stream: true });
      stream.received = (stream.received + chunk).slice(-64 * 1024);
    });
    socket.on("framesent", ({ payload }) => {
      const chunk = typeof payload === "string" ? payload : sentDecoder.decode(payload, { stream: true });
      stream.sent = (stream.sent + chunk).slice(-64 * 1024);
    });
  });
  return {
    commandPromptOccurrences: () =>
      streams.reduce((total, stream) => total + Array.from(stream.received.matchAll(/\x1b\[\d+;\d+H:/g)).length, 0),
    received: (text) => streams.some((stream) => stream.received.includes(text)),
    sent: (text) => streams.some((stream) => stream.sent.includes(text)),
  };
}

async function activeElementDescription(page: Page): Promise<string> {
  return page.evaluate(() => {
    const element = document.activeElement;
    if (!(element instanceof HTMLElement)) return String(element);
    return [
      element.tagName.toLowerCase(),
      element.id ? `#${element.id}` : "",
      ...Array.from(element.classList, (name) => `.${name}`),
    ].join("");
  });
}

// Deny the async clipboard API so the OSC 52 write is observable through the
// HTTP fallback in every browser, the same boundary the clipboard spec uses.
async function interceptClipboardFallback(page: Page): Promise<string[]> {
  await page.addInitScript(() => {
    const denied = () => Promise.reject(new DOMException("denied", "NotAllowedError"));
    Object.defineProperties(navigator.clipboard, {
      write: { configurable: true, value: denied },
      writeText: { configurable: true, value: denied },
    });
  });
  const writes: string[] = [];
  await page.route("**/api/v1/terminal/clipboard", async (route) => {
    const body = route.request().postDataJSON() as { text?: string };
    writes.push(body.text ?? "");
    await route.fulfill({ status: 204 });
  });
  return writes;
}

async function typeMarker(page: Page, marker: string): Promise<void> {
  await page.keyboard.type(`printf '${marker}\\n'`);
  await page.keyboard.press("Enter");
}

async function runTmuxCommand(
  page: Page,
  terminal: ReturnType<typeof observeTerminal>,
  command: string,
): Promise<void> {
  const priorPrompts = terminal.commandPromptOccurrences();
  await page.keyboard.press("Control+b");
  await page.keyboard.press(":");
  await expect.poll(() => terminal.commandPromptOccurrences()).toBeGreaterThan(priorPrompts);
  await page.keyboard.type(command);
  await page.keyboard.press("Enter");
}

test.describe.configure({ mode: "serial", timeout: 120_000 });

test("terminal keeps keyboard focus across a pane move without a click", async ({ page }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  await page.setViewportSize({ width: 1440, height: 900 });

  const fallbackWrites = await interceptClipboardFallback(page);
  const terminal = observeTerminal(page);
  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({
      baseURL: isolatedServer.info.base_url,
    });
    const workspace = await createIssueWorkspace(api);
    // Two real tmux shells in the bottom dock: with two sessions each dock leaf
    // keeps its own "Move ... to a pane" control, so the promotion below runs
    // through the real handler without focusing a palette or button first.
    await launchDockedShell(api, workspace.id);
    await launchDockedShell(api, workspace.id);

    await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
    await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
    const panel = await openTerminalPanel(page);
    const chosenLeaf = panel.locator('.terminal-leaf:has(button[aria-label$=" to a pane"])').first();
    const inlineTerminal = chosenLeaf.locator(".terminal-container");
    await expect(inlineTerminal).toBeVisible();
    await expect(inlineTerminal.locator("canvas, .xterm-screen").first()).toBeVisible();
    const promote = chosenLeaf.getByRole("button", { name: /^Move .+ to a pane$/ });
    await expect(promote).toBeVisible();

    // Promotion is setup for the pane-move scenario. Stamp the exact live
    // xterm node first, so the assertions below prove the pane move carried,
    // rather than rebuilt, the same real tmux terminal.
    await inlineTerminal.evaluate((element) => {
      element.setAttribute("data-focus-reparent-witness", "live-terminal");
    });
    await promote.click();
    const movedTerminal = page.locator('.session-terminal-slot [data-focus-reparent-witness="live-terminal"]');
    await expect(page.locator('.terminal-panel [data-focus-reparent-witness="live-terminal"]')).toHaveCount(0);
    await expect(movedTerminal).toBeVisible();

    await movedTerminal.evaluate((element) => {
      element
        .closest(".tabbed-panel-leaf")
        ?.querySelector<HTMLElement>(".tabbed-panel-tab-button")
        ?.setAttribute("data-focus-drag-source", "true");
    });
    await page
      .locator('[data-pane-key="conversation"]')
      .first()
      .evaluate((element) => {
        element.parentElement?.setAttribute("data-focus-drop-target", "true");
      });

    // Setup only: join the promoted tab into the conversation leaf so the
    // palette's "Split pane right" command has a real structural edit to make.
    await page.evaluate(() => {
      const source = document.querySelector<HTMLElement>('[data-focus-drag-source="true"]');
      const target = document.querySelector<HTMLElement>('[data-focus-drop-target="true"]');
      if (!source || !target) {
        throw new Error("pane drag source or conversation drop target missing");
      }
      const transfer = new DataTransfer();
      const targetRect = target.getBoundingClientRect();
      const sourceRect = source.getBoundingClientRect();
      const dispatch = (type: string, element: HTMLElement, x: number, y: number) =>
        element.dispatchEvent(
          new DragEvent(type, {
            bubbles: true,
            cancelable: true,
            dataTransfer: transfer,
            clientX: x,
            clientY: y,
          }),
        );
      dispatch("dragstart", source, sourceRect.left + sourceRect.width / 2, sourceRect.top + sourceRect.height / 2);
      dispatch("dragover", target, targetRect.left + targetRect.width / 2, targetRect.top + targetRect.height / 2);
      dispatch("drop", target, targetRect.left + targetRect.width / 2, targetRect.top + targetRect.height / 2);
      dispatch("dragend", source, targetRect.left + targetRect.width / 2, targetRect.top + targetRect.height / 2);
    });
    await expect(movedTerminal).toBeVisible();
    await movedTerminal.evaluate((element) => {
      element.closest(".session-terminal-slot")?.setAttribute("data-focus-source-slot", "true");
    });

    // The only terminal click in the scenario under test.
    await movedTerminal.click({ position: { x: 10, y: 10 } });
    await expect.poll(() => activeElementDescription(page)).toContain("xterm-helper-textarea");
    const sourceOwner = page.locator('.tabbed-panel-leaf:has([data-pane-key="conversation"])');
    await expect(sourceOwner).toHaveClass(/input-active/);
    await typeMarker(page, preMarker);
    await expect.poll(() => terminal.sent(preMarker), { timeout: 15_000 }).toBe(true);
    await expect.poll(() => terminal.received(preMarker), { timeout: 15_000 }).toBe(true);
    expect(await activeElementDescription(page)).toContain("xterm-helper-textarea");

    // The real pane command, entirely by keyboard. Palette teardown restores
    // the terminal focus it saved before the handler splits this tab into a
    // new leaf, and the split reparents the terminal through the parking node.
    await page.keyboard.press("Meta+Shift+k");
    const palette = page.getByRole("dialog", { name: "Command palette" });
    await expect(palette).toBeVisible();
    await page.getByRole("textbox", { name: "Search command palette" }).fill("Split pane right");
    await expect(palette.getByRole("button", { name: /Split pane right/ })).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(palette).not.toBeVisible();
    await expect(page.locator('[data-focus-source-slot="true"]')).toHaveCount(0);
    await expect(movedTerminal).toBeVisible();

    // The source leaf must release its keyboard claim while the focused terminal
    // passes through parking. The pool restores focus in the destination, which
    // becomes the layout's only owner; the old source border must not survive.
    await expect.poll(() => activeElementDescription(page)).toContain("xterm-helper-textarea");
    const destinationOwner = page.locator(
      '.tabbed-panel-leaf:has(.session-terminal-slot [data-focus-reparent-witness="live-terminal"])',
    );
    await expect(sourceOwner).not.toHaveClass(/input-active/);
    await expect(destinationOwner).toHaveClass(/input-active/);
    await expect(page.locator(".detail-pane-layout .tabbed-panel-leaf.input-active")).toHaveCount(1);

    // Keystrokes must still reach the live tmux session with no further click.
    await typeMarker(page, postMarker);
    await expect.poll(() => terminal.sent(postMarker), { timeout: 15_000 }).toBe(true);
    await expect.poll(() => terminal.received(postMarker), { timeout: 15_000 }).toBe(true);

    // Keyboard-only tmux clipboard after the move, through the same OSC 52
    // fallback boundary the clipboard spec proves.
    await runTmuxCommand(page, terminal, `set-buffer -w '${clipboardMarker}'`);
    await expect
      .poll(() => fallbackWrites.some((write) => write.includes(clipboardMarker)), {
        timeout: 15_000,
      })
      .toBe(true);
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("terminal acquires keyboard focus on every item switch, not just the first", async ({ page }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  await page.setViewportSize({ width: 1440, height: 900 });

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({
      baseURL: isolatedServer.info.base_url,
    });
    // Two items, each with its own workspace and a live tmux shell filling the
    // workspace pane, so switching items swaps the whole terminal under the
    // detail surface.
    const first = await createIssueWorkspace(api, 10);
    const second = await createIssueWorkspace(api, 13);
    await launchShell(api, first.id, "workflow");
    await launchShell(api, second.id, "workflow");

    // Entering an item with a live terminal must hand it the keyboard without
    // any click: the only prior focus is the page body.
    await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
    await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
    await expect.poll(() => activeElementDescription(page), { timeout: 15_000 }).toContain("xterm-helper-textarea");
    expect(await focusedSessionHost(page)).toContain(first.id);

    // Switching to another item with a workspace must acquire again — the bug
    // was that only the first terminal ever did. The sidebar row is a plain
    // button, exactly the focus a soft request is allowed to take.
    await page.locator("aside button", { hasText: "#13" }).first().click();
    await expect.poll(() => focusedSessionHost(page), { timeout: 15_000 }).toContain(second.id);
    await expect.poll(() => activeElementDescription(page)).toContain("xterm-helper-textarea");

    // And back: re-entry is an acquisition too, not a one-shot.
    await page.locator("aside button", { hasText: "#10" }).first().click();
    await expect.poll(() => focusedSessionHost(page), { timeout: 15_000 }).toContain(first.id);
    await expect.poll(() => activeElementDescription(page)).toContain("xterm-helper-textarea");
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("dock reclaims focus when its focused session stops", async ({ page }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  await page.setViewportSize({ width: 1440, height: 900 });

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const workspace = await createIssueWorkspace(api);
    await launchDockedShell(api, workspace.id);
    await launchDockedShell(api, workspace.id);
    await launchDockedShell(api, workspace.id);
    const sessionKeys = await runningSessionKeys(api, workspace.id);
    expect(sessionKeys).toHaveLength(3);

    await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
    await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
    const panel = await openTerminalPanel(page);
    const focusedTerminal = panel.locator(".terminal-container").first();
    await focusedTerminal.click({ position: { x: 10, y: 10 } });
    const focusedHostKey = await focusedSessionHost(page);
    if (focusedHostKey === null) {
      throw new Error("Expected the focused dock terminal to expose its session host key");
    }
    const focusedSessionKey = sessionKeys.find((key) => focusedHostKey?.includes(key));
    if (focusedSessionKey === undefined) {
      throw new Error(`Focused terminal ${focusedHostKey} did not match a running session`);
    }

    const response = await api.delete(
      `/api/v1/workspaces/${workspace.id}/runtime/sessions/${encodeURIComponent(focusedSessionKey)}`,
      { data: {} },
    );
    expect(response.status(), await response.text()).toBe(204);

    await expect(page.locator(`[data-session-host="${focusedHostKey}"]`)).toHaveCount(0);
    await expect(panel).toBeFocused();
    await expect(panel).toHaveClass(/input-active/);
    await expect(panel.locator(".terminal-container")).toBeVisible();
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});
