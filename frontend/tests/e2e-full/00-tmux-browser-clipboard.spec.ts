import { execFileSync } from "node:child_process";
import { closeSync, constants, openSync, readFileSync, writeSync } from "node:fs";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { load as loadToml } from "js-toml";
import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
} from "@playwright/test";
import { startIsolatedWorkspaceE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  worktree_path: string;
  error_message?: string | null;
};

type TerminalDimensions = {
  columns: number;
  rows: number;
};

type TerminalLinkOpen = {
  url: string;
  target: string | undefined;
  features: string | undefined;
};

type E2ETmuxConfig = {
  tmux?: {
    command?: string[];
  };
};

type RuntimeSessionResponse = {
  sessions: Array<{
    key: string;
    status: string;
  }> | null;
};

type RuntimeAttachSpecResponse = {
  tmux_session: string;
};

let clipboardProbeSequence = 0;
let tmuxClipboardWriteSequence = 0;
const TERMINAL_OUTPUT_TIMEOUT_MS = 15_000;

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

async function createIssueWorkspace(api: APIRequestContext, issueNumber: number): Promise<WorkspaceStatusResponse> {
  const response = await api.post(`/api/v1/issues/github/acme/widgets/${issueNumber}/workspace`, { data: {} });
  expect(response.status()).toBe(202);
  const workspace = (await response.json()) as WorkspaceStatusResponse;
  return waitForWorkspaceReady(api, workspace.id);
}

async function createPullWorkspace(api: APIRequestContext, pullNumber: number): Promise<WorkspaceStatusResponse> {
  const response = await api.post("/api/v1/workspaces", {
    data: {
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widgets",
      mr_number: pullNumber,
    },
  });
  expect(response.status()).toBe(202);
  const workspace = (await response.json()) as WorkspaceStatusResponse;
  return waitForWorkspaceReady(api, workspace.id);
}

async function openPullDetailWithPromotedTerminal(
  page: Page,
  api: APIRequestContext,
  baseUrl: string,
): Promise<{
  workspace: WorkspaceStatusResponse;
  output: ReturnType<typeof observeTerminalOutput>;
  terminal: Locator;
}> {
  const workspace = await createPullWorkspace(api, 1);
  await launchDockedShell(api, workspace.id);
  await launchDockedShell(api, workspace.id);
  const output = observeTerminalOutput(page);

  await page.setViewportSize({ width: 1800, height: 900 });
  await page.goto(`${baseUrl}/pulls/github/acme/widgets/1`);
  await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
  await openTerminalPanel(page);
  const chosenLeaf = page.locator('.terminal-panel.open .terminal-leaf:has(button[aria-label$=" to a pane"])').first();
  await expect(chosenLeaf.locator(".terminal-container")).toBeVisible();
  await chosenLeaf.getByRole("button", { name: /^Move .+ to a pane$/ }).click();
  const terminal = page.locator(".session-terminal-slot .terminal-container");
  await expect(terminal).toBeVisible();

  return { workspace, output, terminal };
}

async function launchDockedShell(api: APIRequestContext, workspaceId: string): Promise<void> {
  const response = await api.post(`/api/v1/workspaces/${workspaceId}/runtime/sessions`, {
    data: {
      target_key: "plain_shell",
      display_region: "terminal",
    },
  });
  expect(response.status(), await response.text()).toBe(200);
}

async function runningRuntimeTmuxSession(api: APIRequestContext, workspaceId: string): Promise<string> {
  const response = await api.get(`/api/v1/workspaces/${workspaceId}/runtime`);
  expect(response.ok()).toBe(true);
  const runtime = (await response.json()) as RuntimeSessionResponse;
  const running = runtime.sessions?.filter((session) => session.status === "running") ?? [];
  expect(running).toHaveLength(1);
  const attachResponse = await api.get(
    `/api/v1/workspaces/${workspaceId}/runtime/sessions/${encodeURIComponent(running[0]!.key)}/attach-spec`,
  );
  expect(attachResponse.ok()).toBe(true);
  return ((await attachResponse.json()) as RuntimeAttachSpecResponse).tmux_session;
}

async function openTerminalPanel(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "Open terminal panel" }).click();
  const container = page.locator(".terminal-panel.open .terminal-container");
  await expect(container).toBeVisible();
  await expect(container.locator("canvas, .xterm-screen").first()).toBeVisible();
  return container;
}

async function selectTopBarTab(page: Page, label: string): Promise<void> {
  const tab = page.locator(".kit-top-bar__tabs .kit-top-bar__tab", { hasText: label });
  if (await tab.isVisible()) {
    await tab.click();
    return;
  }
  await page.locator(".kit-top-bar__nav-select .kit-select-dropdown__trigger").click();
  await page.getByRole("option", { name: label, exact: true }).click();
}

async function selectIssueByTitle(page: Page, title: string): Promise<void> {
  await selectTopBarTab(page, "Issues");
  const issue = page.locator(".issue-item").filter({ hasText: title }).first();
  await issue.click();
  await expect(page.locator(".issue-detail")).toBeVisible();
}

function observeTerminalOutput(page: Page): {
  includes(text: string): boolean;
  count(text: string): number;
  inputIncludes(text: string): boolean;
  dimensionsForLatestInput(): TerminalDimensions | null;
  activeSocketCount(): number;
} {
  let inputSequence = 0;
  const streams: Array<{
    output: string;
    input: string;
    columns: number | null;
    rows: number | null;
    lastInputSequence: number;
    closed: boolean;
  }> = [];
  page.on("websocket", (socket) => {
    const stream = {
      output: "",
      input: "",
      columns: null as number | null,
      rows: null as number | null,
      lastInputSequence: 0,
      closed: false,
    };
    streams.push(stream);
    const socketUrl = new URL(socket.url());
    const initialColumns = Number.parseInt(socketUrl.searchParams.get("cols") ?? "", 10);
    const initialRows = Number.parseInt(socketUrl.searchParams.get("rows") ?? "", 10);
    if (Number.isFinite(initialColumns) && initialColumns > 0) {
      stream.columns = initialColumns;
    }
    if (Number.isFinite(initialRows) && initialRows > 0) {
      stream.rows = initialRows;
    }
    const decoder = new TextDecoder();
    socket.on("framereceived", ({ payload }) => {
      const chunk = typeof payload === "string" ? payload : decoder.decode(payload, { stream: true });
      stream.output = (stream.output + chunk).slice(-64 * 1024);
    });
    socket.on("framesent", ({ payload }) => {
      let recognizedControl = false;
      if (typeof payload === "string") {
        try {
          const control = JSON.parse(payload) as {
            type?: string;
            cols?: number;
            rows?: number;
          };
          if (control.type === "resize" || control.type === "refresh") {
            recognizedControl = true;
            if (typeof control.cols === "number" && control.cols > 0) {
              stream.columns = control.cols;
            }
            if (typeof control.rows === "number" && control.rows > 0) {
              stream.rows = control.rows;
            }
          }
        } catch {
          // Raw string terminal input is not a JSON control.
        }
      }
      if (!recognizedControl) {
        const chunk = typeof payload === "string" ? payload : new TextDecoder().decode(payload);
        stream.input = (stream.input + chunk).slice(-64 * 1024);
        inputSequence += 1;
        stream.lastInputSequence = inputSequence;
      }
    });
    socket.on("close", () => {
      stream.closed = true;
    });
  });
  return {
    includes: (text: string) => streams.some((stream) => stream.output.includes(text)),
    count: (text: string) => streams.reduce((total, stream) => total + stream.output.split(text).length - 1, 0),
    inputIncludes: (text: string) => streams.some((stream) => stream.input.includes(text)),
    activeSocketCount: () => streams.filter((stream) => !stream.closed).length,
    dimensionsForLatestInput: () => {
      let latestStream: (typeof streams)[number] | null = null;
      for (const stream of streams) {
        if (!latestStream || stream.lastInputSequence > latestStream.lastInputSequence) {
          latestStream = stream;
        }
      }
      return latestStream?.columns && latestStream.rows
        ? { columns: latestStream.columns, rows: latestStream.rows }
        : null;
    },
  };
}

function shellOctal(text: string): string {
  return Array.from(new TextEncoder().encode(text), (byte) => `\\${byte.toString(8).padStart(3, "0")}`).join("");
}

function runE2ETmuxCommand(server: IsolatedE2EServer, args: string[]): string {
  const config = loadToml(readFileSync(server.info.config_path, "utf8")) as E2ETmuxConfig;
  const [command, ...prefix] = config.tmux?.command ?? [];
  if (!command) throw new Error("e2e tmux command is unavailable");
  return execFileSync(command, [...prefix, ...args], { encoding: "utf8" }).trim();
}

function writeTmuxClientTTY(server: IsolatedE2EServer, tmuxSession: string, data: Uint8Array): void {
  const clientTTY = runE2ETmuxCommand(server, ["list-clients", "-t", tmuxSession, "-F", "#{client_tty}"]);
  const fd = openSync(clientTTY, constants.O_WRONLY | constants.O_NOCTTY);
  try {
    writeSync(fd, data);
  } finally {
    closeSync(fd);
  }
}

async function interceptTerminalLinkOpens(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const testWindow = window as Window & { __terminalLinkOpens?: TerminalLinkOpen[] };
    testWindow.__terminalLinkOpens = [];
    window.open = ((url?: string | URL, target?: string, features?: string) => {
      testWindow.__terminalLinkOpens!.push({
        url: String(url ?? ""),
        target,
        features,
      });
      return null;
    }) as typeof window.open;
  });
}

async function terminalLinkOpens(page: Page): Promise<TerminalLinkOpen[]> {
  return await page.evaluate(() => {
    const testWindow = window as Window & { __terminalLinkOpens?: TerminalLinkOpen[] };
    return testWindow.__terminalLinkOpens ?? [];
  });
}

async function renderTerminalLink(
  page: Page,
  container: Locator,
  output: ReturnType<typeof observeTerminalOutput>,
  renderedText: string,
  marker: string,
): Promise<TerminalDimensions> {
  await container.locator(".xterm-helper-textarea").focus();
  await page.keyboard.type(`clear; printf '${shellOctal(`${renderedText}\n${marker}\n`)}'`);
  await page.keyboard.press("Enter");
  await expect.poll(() => output.includes(marker), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
      }),
  );
  const dimensions = output.dimensionsForLatestInput();
  if (!dimensions) throw new Error(`terminal dimensions unavailable for link marker ${marker}`);
  return dimensions;
}

async function renderRawTerminalLink(
  page: Page,
  output: ReturnType<typeof observeTerminalOutput>,
  server: IsolatedE2EServer,
  tmuxSession: string,
  renderedText: string,
  marker: string,
): Promise<TerminalDimensions> {
  writeTmuxClientTTY(server, tmuxSession, new TextEncoder().encode(`\x1b[2J\x1b[H${renderedText}\r\n${marker}\r\n`));
  await expect.poll(() => output.includes(marker), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
      }),
  );
  const dimensions = output.dimensionsForLatestInput();
  if (!dimensions) throw new Error(`terminal dimensions unavailable for link marker ${marker}`);
  return dimensions;
}

async function terminalCellCenter(
  container: Locator,
  dimensions: TerminalDimensions,
  column: number,
  row: number,
): Promise<{ x: number; y: number }> {
  return await container.evaluate(
    (element, { columns, rows, columnIndex, rowIndex }) => {
      const screen = element.querySelector<HTMLElement>(".xterm-screen");
      if (!screen) throw new Error("xterm screen geometry unavailable");
      const bounds = screen.getBoundingClientRect();
      return {
        x: bounds.left + (bounds.width / columns) * (columnIndex + 0.5),
        y: bounds.top + (bounds.height / rows) * (rowIndex + 0.5),
      };
    },
    { columns: dimensions.columns, rows: dimensions.rows, columnIndex: column, rowIndex: row },
  );
}

async function emitApplicationOsc52(
  page: Page,
  container: Locator,
  output: ReturnType<typeof observeTerminalOutput>,
): Promise<void> {
  const marker = "application osc52 complete";
  const payload = "dW50cnVzdGVkIGNsaXBib2FyZCB2YWx1ZQ==";
  await container.click({ position: { x: 10, y: 10 } });
  await page.keyboard.type(`printf '${shellOctal(`\x1b]52;c;${payload}\x07${marker}\n`)}'`);
  await page.keyboard.press("Enter");
  await expect.poll(() => output.includes(marker), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
}

async function scheduleTmuxClipboardWrite(page: Page, container: Locator): Promise<string> {
  await container.click({ position: { x: 10, y: 10 } });
  return typeScheduledTmuxClipboardWrite(page);
}

async function typeScheduledTmuxClipboardWrite(page: Page): Promise<string> {
  const marker = "late terminal osc52 complete";
  const value = "late terminal write";
  tmuxClipboardWriteSequence += 1;
  const channel = `kenn-forge-clipboard-write-${process.pid}-${tmuxClipboardWriteSequence}`;
  await runAttachedTmuxCommand(
    page,
    `run-shell -b "while [ ! -f .clipboard-osc52-gate ]; do sleep 0.05; done; tmux wait-for -S '${channel}'" ; wait-for '${channel}' ; set-buffer -w '${value}' ; display-message '${marker}'`,
  );
  return marker;
}

async function runAttachedTmuxCommand(page: Page, command: string): Promise<void> {
  await page.keyboard.press("Control+b");
  await page.keyboard.press(":");
  await page.keyboard.type(command);
  await page.keyboard.press("Enter");
}

async function runClipboardGesture(page: Page, action: "read" | "write", text = ""): Promise<string> {
  clipboardProbeSequence += 1;
  const probeId = `kenn-forge-clipboard-probe-${clipboardProbeSequence}`;
  await page.evaluate(
    ({ id, operation, value }) => {
      const button = document.createElement("button");
      button.id = id;
      button.textContent = "clipboard probe";
      button.style.position = "fixed";
      button.style.left = "0";
      button.style.top = "0";
      button.style.zIndex = "2147483647";
      button.dataset.state = "pending";
      button.addEventListener(
        "click",
        async () => {
          try {
            button.dataset.result =
              operation === "read"
                ? await navigator.clipboard.readText()
                : (await navigator.clipboard.writeText(value), "");
            button.dataset.state = "done";
          } catch (error) {
            button.dataset.state = "error";
            button.dataset.error = error instanceof Error ? `${error.name}: ${error.message}` : String(error);
          }
        },
        { once: true },
      );
      document.body.appendChild(button);
    },
    { id: probeId, operation: action, value: text },
  );

  const probe = page.locator(`#${probeId}`);
  await probe.click();
  await expect.poll(() => probe.getAttribute("data-state")).not.toBe("pending");
  const state = await probe.getAttribute("data-state");
  const result = (await probe.getAttribute("data-result")) ?? "";
  const error = await probe.getAttribute("data-error");
  await probe.evaluate((element) => element.remove());
  if (state === "error") throw new Error(error ?? "clipboard gesture failed");
  return result;
}

async function setBrowserClipboard(page: Page, text: string): Promise<void> {
  await runClipboardGesture(page, "write", text);
}

async function readBrowserClipboard(page: Page): Promise<string> {
  return runClipboardGesture(page, "read");
}

async function interceptDeniedBrowserClipboard(page: Page): Promise<string[]> {
  await page.addInitScript(() => {
    const denied = () => Promise.reject(new DOMException("denied", "NotAllowedError"));
    Object.defineProperties(navigator.clipboard, {
      write: { configurable: true, value: denied },
      writeText: { configurable: true, value: denied },
    });
  });
  return captureTerminalClipboardFallback(page);
}

async function denyCurrentPageBrowserClipboardWrites(page: Page): Promise<void> {
  await page.evaluate(() => {
    const clipboard = navigator.clipboard;
    if (!clipboard) return;
    const denied = () => Promise.reject(new DOMException("denied", "NotAllowedError"));
    Object.defineProperties(clipboard, {
      write: { configurable: true, value: denied },
      writeText: { configurable: true, value: denied },
    });
  });
}

async function captureTerminalClipboardFallback(page: Page): Promise<string[]> {
  const fallbackWrites: string[] = [];
  await page.route("**/api/v1/terminal/clipboard", async (route) => {
    const request = route.request();
    await expect(request.headerValue("content-type")).resolves.toContain("application/json");
    await expect(request.headerValue("x-kenn-forge-csrf")).resolves.toBe("1");
    const body = request.postDataJSON() as { text?: string };
    fallbackWrites.push(body.text ?? "");
    await route.fulfill({ status: 204 });
  });
  return fallbackWrites;
}

async function renderMarker(
  page: Page,
  container: Locator,
  output: ReturnType<typeof observeTerminalOutput>,
  marker: string,
): Promise<TerminalDimensions> {
  await container.click({ position: { x: 10, y: 10 } });
  await page.keyboard.type(`clear; printf '${shellOctal(`${marker}#`)}\\n'`);
  await page.keyboard.press("Enter");
  await expect.poll(() => output.includes(marker), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
  // The Playwright WebSocket observer can see the marker before xterm has
  // parsed and painted the preceding clear sequence. Wait for two browser
  // frames so pointer coordinates target the rendered marker row.
  await page.waitForTimeout(500);
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
      }),
  );
  const dimensions = output.dimensionsForLatestInput();
  if (!dimensions) {
    throw new Error(`terminal dimensions unavailable for marker ${marker}`);
  }
  return dimensions;
}

async function enableTmuxMouseAndRenderMarker(
  page: Page,
  container: Locator,
  output: ReturnType<typeof observeTerminalOutput>,
  marker: string,
): Promise<TerminalDimensions> {
  await container.click({ position: { x: 10, y: 10 } });
  await runAttachedTmuxCommand(page, "set-option -g mouse off");
  await runAttachedTmuxCommand(page, "set-option -g mouse on");
  await expect(container.locator(".xterm.enable-mouse-events")).toBeVisible();
  return renderMarker(page, container, output, marker);
}

async function copyMarkerWithTmuxKeys(
  page: Page,
  container: Locator,
  output: ReturnType<typeof observeTerminalOutput>,
  marker: string,
): Promise<void> {
  await renderMarker(page, container, output, marker);
  await runAttachedTmuxCommand(page, `set-buffer -w '${marker}'`);
}

async function dragTerminalCells(page: Page, container: Locator, cellCount: number): Promise<void> {
  const points = await container.evaluate((element, count) => {
    const screen = element.querySelector<HTMLElement>(".xterm-screen");
    const textarea = element.querySelector<HTMLElement>(".xterm-helper-textarea");
    if (!screen || !textarea) {
      throw new Error("xterm cell geometry unavailable");
    }
    const bounds = screen.getBoundingClientRect();
    // xterm sizes its helper textarea to one rendered cell. Measuring it
    // avoids coupling pointer coordinates to an asynchronously delivered
    // backend resize message.
    const cellBounds = textarea.getBoundingClientRect();
    const cellWidth = cellBounds.width;
    const cellHeight = cellBounds.height;
    if (cellWidth <= 0 || cellHeight <= 0) {
      throw new Error("xterm cell geometry is empty");
    }
    return {
      start: {
        x: bounds.left + cellWidth * 0.5,
        y: bounds.top + cellHeight * 0.5,
      },
      end: {
        // renderMarker prints "#" after the marker. Drag through that
        // sentinel so platform-specific endpoint rounding cannot omit the
        // marker's final cell.
        x: bounds.left + cellWidth * (count + 0.5),
        y: bounds.top + cellHeight * 0.5,
      },
    };
  }, cellCount);

  await page.mouse.move(points.start.x, points.start.y);
  await beginCapturedPointerGesture(page, container);
  await page.mouse.move(points.end.x, points.end.y, { steps: 10 });
  await page.mouse.up();
}

async function clickTerminalWithPointerJitter(page: Page, container: Locator, horizontalJitter = 5): Promise<void> {
  const start = await container.evaluate((element) => {
    const screen = element.querySelector<HTMLElement>(".xterm-screen");
    const textarea = element.querySelector<HTMLElement>(".xterm-helper-textarea");
    if (!screen || !textarea) throw new Error("xterm cell geometry unavailable");
    const bounds = screen.getBoundingClientRect();
    const cellBounds = textarea.getBoundingClientRect();
    if (cellBounds.width <= 0 || cellBounds.height <= 0) {
      throw new Error("xterm cell geometry is empty");
    }
    return {
      x: bounds.left + cellBounds.width * 0.5,
      y: bounds.top + cellBounds.height * 0.5,
    };
  });

  await page.mouse.move(start.x, start.y);
  await beginCapturedPointerGesture(page, container);
  // A physical focus click can slip a few pixels while the button is down.
  await page.mouse.move(start.x + horizontalJitter, start.y);
  await page.mouse.up();
}

async function beginCapturedPointerGesture(page: Page, container: Locator): Promise<void> {
  await container.evaluate((element) => {
    const target = element as HTMLElement;
    target.addEventListener(
      "pointerdown",
      (event) => {
        target.dataset.playwrightPointerId = String(event.pointerId);
      },
      { capture: true, once: true },
    );
  });
  await page.mouse.down();
  await expect
    .poll(() =>
      container.evaluate((element) => {
        const target = element as HTMLElement;
        const pointerId = Number(target.dataset.playwrightPointerId);
        return Number.isInteger(pointerId) && target.hasPointerCapture(pointerId);
      }),
    )
    .toBe(true);
}

async function renderScrollMarkers(
  page: Page,
  container: Locator,
  output: ReturnType<typeof observeTerminalOutput>,
): Promise<TerminalDimensions> {
  const lastMarker = "edge-scroll-080";
  await container.click({ position: { x: 10, y: 10 } });
  await page.keyboard.type("clear; i=1; while [ $i -le 80 ]; do printf 'edge-scroll-%03d\\n' \"$i\"; i=$((i+1)); done");
  await page.keyboard.press("Enter");
  await expect.poll(() => output.includes(lastMarker), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
  const dimensions = output.dimensionsForLatestInput();
  if (!dimensions) {
    throw new Error("terminal dimensions unavailable for edge-scroll markers");
  }
  return dimensions;
}

async function dragTerminalPastTop(page: Page, container: Locator, dimensions: TerminalDimensions): Promise<void> {
  const points = await container.evaluate((element, { columns, rows }) => {
    const screen = element.querySelector<HTMLElement>(".xterm-screen");
    if (!screen) {
      throw new Error("xterm cell geometry unavailable");
    }
    const bounds = screen.getBoundingClientRect();
    const cellWidth = bounds.width / columns;
    const cellHeight = bounds.height / rows;
    if (cellWidth <= 0 || cellHeight <= 0) {
      throw new Error("xterm cell geometry is empty");
    }
    return {
      start: {
        x: bounds.left + cellWidth * 0.5,
        y: bounds.bottom - cellHeight * 1.5,
      },
      end: {
        x: bounds.left + cellWidth * 0.5,
        y: bounds.top - cellHeight * 2,
      },
    };
  }, dimensions);

  await page.mouse.move(points.start.x, points.start.y);
  await page.mouse.down();
  await page.mouse.move(points.end.x, points.end.y, { steps: 10 });
  await page.waitForTimeout(7_000);
  await page.mouse.up();
}

test.describe.configure({ mode: "serial", timeout: 120_000 });

test("real xterm links disclose destinations and require a modified click", async ({ page }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    await interceptTerminalLinkOpens(page);
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const workspace = await createIssueWorkspace(api, 10);
    const output = observeTerminalOutput(page);

    await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
    const terminal = await openTerminalPanel(page);
    const tmuxSession = await runningRuntimeTmuxSession(api, workspace.id);
    const usesMetaKey = await page.evaluate(() => /Mac/.test(navigator.platform));
    const modifier = usesMetaKey ? "Meta" : "Control";
    const modifierLabel = usesMetaKey ? "Cmd" : "Ctrl";
    const links = [
      {
        destination: "https://example.com/detected-link",
        renderedText: "https://example.com/detected-link",
        marker: "detected-link-ready",
        raw: false,
      },
      {
        destination: "https://example.org/osc8-destination",
        renderedText: "\x1b]8;;https://example.org/osc8-destination\x07Open release notes\x1b]8;;\x07",
        marker: "osc8-link-ready",
        raw: true,
      },
    ];

    for (const link of links) {
      await page.mouse.move(1, 1);
      await expect(terminal.locator(".terminal-link-tooltip")).toHaveCount(0);
      const dimensions = link.raw
        ? await renderRawTerminalLink(page, output, isolatedServer, tmuxSession, link.renderedText, link.marker)
        : await renderTerminalLink(page, terminal, output, link.renderedText, link.marker);
      expect(output.includes(link.destination), `${link.marker} destination did not reach the terminal stream`).toBe(
        true,
      );
      const point = await terminalCellCenter(terminal, dimensions, 4, 0);
      const resetPoint = await terminalCellCenter(terminal, dimensions, 4, 2);

      await page.mouse.move(resetPoint.x, resetPoint.y);
      await page.mouse.move(point.x, point.y);
      const tooltip = terminal.locator(".terminal-link-tooltip");
      await expect(tooltip).toContainText(link.destination);
      await expect(tooltip).toContainText(`${modifierLabel}+Click to open link`);

      const opensBeforeClick = await terminalLinkOpens(page);
      await page.mouse.click(point.x, point.y);
      expect(await terminalLinkOpens(page)).toEqual(opensBeforeClick);

      await page.mouse.move(resetPoint.x, resetPoint.y);
      await expect(tooltip).toHaveCount(0);
      await page.mouse.move(point.x, point.y);
      await expect(tooltip).toContainText(link.destination);
      await page.keyboard.down(modifier);
      await page.mouse.click(point.x, point.y);
      await page.keyboard.up(modifier);
      await expect.poll(() => terminalLinkOpens(page)).toHaveLength(opensBeforeClick.length + 1);
    }

    await expect
      .poll(() => terminalLinkOpens(page))
      .toEqual(
        links.map((link) => ({
          url: link.destination,
          target: "_blank",
          features: "noopener,noreferrer",
        })),
      );
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("modified non-link terminal drags retain pointer capture through outside release", async ({ page }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  let modifierDown = false;
  let pointerDown = false;
  let modifier: "Meta" | "Control" | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const workspace = await createIssueWorkspace(api, 10);

    await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
    const terminal = await openTerminalPanel(page);
    const screen = await terminal.locator(".xterm-screen").boundingBox();
    if (!screen) throw new Error("xterm screen geometry unavailable");
    modifier = (await page.evaluate(() => /Mac/.test(navigator.platform))) ? "Meta" : "Control";

    await page.mouse.move(screen.x + screen.width / 2, screen.y + screen.height / 2);
    await page.keyboard.down(modifier);
    modifierDown = true;
    pointerDown = true;
    await beginCapturedPointerGesture(page, terminal);

    await page.mouse.move(screen.x - 20, screen.y - 20, { steps: 5 });
    await page.mouse.up();
    pointerDown = false;
    await expect
      .poll(() =>
        terminal.evaluate((element) => {
          const target = element as HTMLElement;
          return target.hasPointerCapture(Number(target.dataset.playwrightPointerId));
        }),
      )
      .toBe(false);
  } finally {
    if (pointerDown) await page.mouse.up();
    if (modifierDown && modifier) await page.keyboard.up(modifier);
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("tmux blocks application OSC 52 while keyboard copy reaches the clipboard", async ({ page, browserName }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  let fallbackWrites: string[] = [];
  if (browserName === "chromium") {
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  } else {
    fallbackWrites = await interceptDeniedBrowserClipboard(page);
  }

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({
      baseURL: isolatedServer.info.base_url,
    });
    const workspace = await createIssueWorkspace(api, 10);
    const output = observeTerminalOutput(page);

    await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
    const tabTerminal = await openTerminalPanel(page);
    const keyboardMarker = "keyboard clipboard marker";
    if (browserName === "firefox") {
      await emitApplicationOsc52(page, tabTerminal, output);
      await page.waitForTimeout(250);
      expect(fallbackWrites).toEqual([]);

      await copyMarkerWithTmuxKeys(page, tabTerminal, output, keyboardMarker);
      await expect.poll(() => fallbackWrites).toContain(keyboardMarker);
    } else {
      await setBrowserClipboard(page, "trusted clipboard value");
      await emitApplicationOsc52(page, tabTerminal, output);
      await page.waitForTimeout(250);
      expect(await readBrowserClipboard(page)).toBe("trusted clipboard value");

      await setBrowserClipboard(page, "");
      await copyMarkerWithTmuxKeys(page, tabTerminal, output, keyboardMarker);
      await expect.poll(() => readBrowserClipboard(page), { timeout: 15_000 }).toBe(keyboardMarker);
    }
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("visible terminal focus loss revokes keyboard authorization after a missed pointer release", async ({
  page,
  browserName,
}) => {
  test.skip(browserName !== "chromium", "Clipboard ordering assertions require Chromium permissions");
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const workspace = await createIssueWorkspace(api, 10);
    await launchDockedShell(api, workspace.id);
    await launchDockedShell(api, workspace.id);
    const output = observeTerminalOutput(page);

    await page.setViewportSize({ width: 1800, height: 900 });
    await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
    await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
    await openTerminalPanel(page);
    const chosenLeaf = page
      .locator('.terminal-panel.open .terminal-leaf:has(button[aria-label$=" to a pane"])')
      .first();
    const dockedTerminal = chosenLeaf.locator(".terminal-container");
    await expect(dockedTerminal).toBeVisible();
    await chosenLeaf.getByRole("button", { name: /^Move .+ to a pane$/ }).click();
    const terminal = page.locator(".session-terminal-slot .terminal-container");
    await expect(terminal).toBeVisible();

    const bounds = await terminal.boundingBox();
    if (!bounds) throw new Error("promoted terminal has no pointer bounds");
    await page.mouse.move(bounds.x + 10, bounds.y + 10);
    await page.evaluate(() => {
      const nativeSetTimeout = window.setTimeout.bind(window);
      const testWindow = window as Window & { __restoreNativeSetTimeout?: () => void };
      testWindow.__restoreNativeSetTimeout = () => {
        window.setTimeout = nativeSetTimeout;
      };
      window.setTimeout = ((handler: TimerHandler, timeout?: number, ...args: unknown[]) =>
        nativeSetTimeout(handler, timeout === 60_000 ? 500 : timeout, ...args)) as typeof window.setTimeout;
    });
    await beginCapturedPointerGesture(page, terminal);
    await page.evaluate(() => {
      (window as Window & { __restoreNativeSetTimeout?: () => void }).__restoreNativeSetTimeout?.();
    });
    await page.waitForTimeout(750);
    await expect
      .poll(() =>
        terminal.evaluate((element) => {
          const target = element as HTMLElement;
          return target.hasPointerCapture(Number(target.dataset.playwrightPointerId));
        }),
      )
      .toBe(false);
    await page.mouse.up();

    const lateWriteMarker = await typeScheduledTmuxClipboardWrite(page);
    const copyButton = page.getByRole("button", { name: "Copy issue #10 link" });
    await copyButton.click();
    await expect(copyButton).toHaveAttribute("title", "Copied!");
    await expect.poll(() => readBrowserClipboard(page)).toBe("https://github.com/acme/widgets/issues/10");

    await writeFile(join(workspace.worktree_path, ".clipboard-osc52-gate"), "go", { mode: 0o600 });
    await expect.poll(() => output.includes(lateWriteMarker), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
    await expect.poll(() => output.includes("\x1b]52;"), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
    await page.waitForTimeout(250);
    expect(await readBrowserClipboard(page)).toBe("https://github.com/acme/widgets/issues/10");
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("visible detail copy wins when focus leaves a pointer-captured terminal", async ({ page, browserName }) => {
  test.skip(browserName !== "chromium", "Clipboard ordering assertions require Chromium permissions");
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  let pointerIsDown = false;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const workspace = await createIssueWorkspace(api, 10);
    await launchDockedShell(api, workspace.id);
    await launchDockedShell(api, workspace.id);
    const output = observeTerminalOutput(page);

    await page.setViewportSize({ width: 1800, height: 900 });
    await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
    await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
    await openTerminalPanel(page);
    const chosenLeaf = page
      .locator('.terminal-panel.open .terminal-leaf:has(button[aria-label$=" to a pane"])')
      .first();
    const dockedTerminal = chosenLeaf.locator(".terminal-container");
    await expect(dockedTerminal).toBeVisible();
    await chosenLeaf.getByRole("button", { name: /^Move .+ to a pane$/ }).click();
    const terminal = page.locator(".session-terminal-slot .terminal-container");
    await expect(terminal).toBeVisible();

    const lateWriteMarker = await scheduleTmuxClipboardWrite(page, terminal);
    const bounds = await terminal.boundingBox();
    if (!bounds) throw new Error("promoted terminal has no pointer bounds");
    await page.mouse.move(bounds.x + 10, bounds.y + 10);
    await beginCapturedPointerGesture(page, terminal);
    pointerIsDown = true;

    const copyButton = page.getByRole("button", { name: "Copy issue #10 link" });
    await copyButton.evaluate((element) => (element as HTMLElement).focus());
    await expect.poll(() => copyButton.evaluate((element) => document.activeElement === element)).toBe(true);
    await expect
      .poll(() =>
        terminal.evaluate((element) => {
          const target = element as HTMLElement;
          return target.hasPointerCapture(Number(target.dataset.playwrightPointerId));
        }),
      )
      .toBe(false);
    await page.keyboard.press("Enter");
    await expect(copyButton).toHaveAttribute("title", "Copied!");

    await writeFile(join(workspace.worktree_path, ".clipboard-osc52-gate"), "go", { mode: 0o600 });
    await expect.poll(() => output.includes(lateWriteMarker), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
    await expect.poll(() => output.includes("\x1b]52;"), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
    await page.waitForTimeout(250);
    await page.mouse.up();
    pointerIsDown = false;
    expect(await readBrowserClipboard(page)).toBe("https://github.com/acme/widgets/issues/10");
  } finally {
    if (pointerIsDown) await page.mouse.up();
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("PR comment copy survives 5px terminal focus jitter at default geometry", async ({ page, browserName }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  let fallbackWrites: string[] = [];
  if (browserName === "chromium") {
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  } else {
    fallbackWrites = await captureTerminalClipboardFallback(page);
  }

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const { output, terminal } = await openPullDetailWithPromotedTerminal(page, api, isolatedServer.info.base_url);

    await enableTmuxMouseAndRenderMarker(page, terminal, output, "focus click marker");
    const copiedComment =
      "Guard the cache fallback before returning stale data.\n\nExpanded context explains stale data handling.";
    const commentCard = page.locator(".pull-detail .kit-comment-card", {
      hasText: "Guard the cache fallback before returning stale data.",
    });
    await expect(commentCard).toBeVisible();
    const copyButton = commentCard
      .locator(":scope > .kit-card__header > .kit-card__actions")
      .locator('button[aria-label="Copy comment"], button[aria-label="Copied"]');
    await copyButton.evaluate((element) => element.scrollIntoView({ block: "center", inline: "nearest" }));
    await commentCard.hover();
    await copyButton.click();
    await expect(copyButton).toHaveAttribute("aria-label", "Copied");
    if (browserName === "firefox") await denyCurrentPageBrowserClipboardWrites(page);

    const osc52Count = output.count("\x1b]52;");
    const cellWidth = await terminal
      .locator(".xterm-helper-textarea")
      .evaluate((element) => element.getBoundingClientRect().width);
    expect(cellWidth).toBeGreaterThan(5);
    await clickTerminalWithPointerJitter(page, terminal);
    await expect
      .poll(() => output.count("\x1b]52;"), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS })
      .toBeGreaterThan(osc52Count);
    await page.waitForTimeout(250);
    if (browserName === "firefox") {
      expect(fallbackWrites).toEqual([]);
    } else {
      expect(await readBrowserClipboard(page)).toBe(copiedComment);
    }
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("outside PR comment copy revokes a delayed terminal write before focus changes", async ({ page, browserName }) => {
  test.skip(browserName !== "chromium", "Clipboard ordering assertions require Chromium permissions");
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const { workspace, output, terminal } = await openPullDetailWithPromotedTerminal(
      page,
      api,
      isolatedServer.info.base_url,
    );

    const lateWriteMarker = await scheduleTmuxClipboardWrite(page, terminal);
    const copiedComment =
      "Guard the cache fallback before returning stale data.\n\nExpanded context explains stale data handling.";
    const commentCard = page.locator(".pull-detail .kit-comment-card", {
      hasText: "Guard the cache fallback before returning stale data.",
    });
    await expect(commentCard).toBeVisible();
    const copyButton = commentCard
      .locator(":scope > .kit-card__header > .kit-card__actions")
      .locator('button[aria-label="Copy comment"], button[aria-label="Copied"]');
    await copyButton.evaluate((element) => element.scrollIntoView({ block: "center", inline: "nearest" }));
    await commentCard.hover();
    expect(await terminal.evaluate((element) => element.contains(document.activeElement))).toBe(true);
    // Keep DOM focus in the terminal so only outside pointerdown revocation can
    // prevent the delayed terminal write from racing the real comment copy.
    await copyButton.evaluate((element) => {
      element.addEventListener("mousedown", (event) => event.preventDefault(), { once: true });
    });
    await copyButton.click();
    await expect(copyButton).toHaveAttribute("aria-label", "Copied");
    expect(await terminal.evaluate((element) => element.contains(document.activeElement))).toBe(true);

    await writeFile(join(workspace.worktree_path, ".clipboard-osc52-gate"), "go", { mode: 0o600 });
    await expect.poll(() => output.includes(lateWriteMarker), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
    await expect.poll(() => output.includes("\x1b]52;"), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
    await page.waitForTimeout(250);
    expect(await readBrowserClipboard(page)).toBe(copiedComment);
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("a parked pooled terminal cannot overwrite a newer detail clipboard copy", async ({ page, browserName }) => {
  test.skip(browserName !== "chromium", "Clipboard ordering assertions require Chromium permissions");
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({
      baseURL: isolatedServer.info.base_url,
    });
    const workspace = await createIssueWorkspace(api, 10);
    await launchDockedShell(api, workspace.id);
    await launchDockedShell(api, workspace.id);
    const output = observeTerminalOutput(page);

    await page.setViewportSize({ width: 1800, height: 900 });
    await page.goto(`${isolatedServer.info.base_url}/issues/github/acme/widgets/10`);
    await expect(page.locator(".detail-pane-workspace-slot .workspace-host-wrapper")).toBeVisible();
    await openTerminalPanel(page);
    const chosenLeaf = page
      .locator('.terminal-panel.open .terminal-leaf:has(button[aria-label$=" to a pane"])')
      .first();
    const dockedTerminal = chosenLeaf.locator(".terminal-container");
    await expect(dockedTerminal).toBeVisible();
    const promote = chosenLeaf.getByRole("button", { name: /^Move .+ to a pane$/ });
    await expect(promote).toBeVisible();
    await dockedTerminal.evaluate((element) => element.setAttribute("data-clipboard-park-witness", "live-terminal"));
    await promote.click();

    const terminal = page.locator('.session-terminal-slot [data-clipboard-park-witness="live-terminal"]');
    await expect(terminal).toBeVisible();

    const lateWriteMarker = await scheduleTmuxClipboardWrite(page, terminal);
    await expect.poll(() => output.activeSocketCount()).toBeGreaterThan(0);
    const activeSocketsBeforePark = output.activeSocketCount();
    const promotedLeaf = page.locator(".tabbed-panel-leaf").filter({ has: terminal });
    await promotedLeaf
      .locator('[data-testid^="pane-hide-session:"]')
      .evaluate((element) => (element as HTMLElement).click());
    await expect(page.locator('.session-pool-parking [data-clipboard-park-witness="live-terminal"]')).toHaveCount(1);
    await expect.poll(() => output.activeSocketCount()).toBe(activeSocketsBeforePark);

    const copyButton = page.getByRole("button", { name: "Copy issue #10 link" });
    await copyButton.click();
    await expect(copyButton).toHaveAttribute("title", "Copied!");
    await expect.poll(() => readBrowserClipboard(page)).toBe("https://github.com/acme/widgets/issues/10");

    await writeFile(join(workspace.worktree_path, ".clipboard-osc52-gate"), "go", { mode: 0o600 });
    await expect.poll(() => output.includes(lateWriteMarker), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
    await expect.poll(() => output.includes("\x1b]52;"), { timeout: TERMINAL_OUTPUT_TIMEOUT_MS }).toBe(true);
    await page.waitForTimeout(250);
    expect(await readBrowserClipboard(page)).toBe("https://github.com/acme/widgets/issues/10");
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("tmux drag-copy reaches the clipboard in tab and inline hosts", async ({ page, browserName }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  let fallbackWrites: string[] = [];
  if (browserName === "chromium") {
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  } else {
    fallbackWrites = await interceptDeniedBrowserClipboard(page);
  }

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({
      baseURL: isolatedServer.info.base_url,
    });
    const workspace = await createIssueWorkspace(api, 10);
    const output = observeTerminalOutput(page);

    await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
    const tabTerminal = await openTerminalPanel(page);
    const tabMarker = "tab clipboard — marker\u00a0value";
    await enableTmuxMouseAndRenderMarker(page, tabTerminal, output, tabMarker);
    await dragTerminalCells(page, tabTerminal, tabMarker.length);
    if (browserName === "firefox") {
      await expect.poll(() => fallbackWrites.some((write) => write.includes(tabMarker))).toBe(true);
      fallbackWrites.length = 0;
    } else {
      await expect.poll(() => readBrowserClipboard(page)).toContain(tabMarker);
      await setBrowserClipboard(page, "");
    }

    await page.locator(".terminal-panel.open").getByRole("button", { name: "Close terminal panel" }).first().click();
    await expect(tabTerminal).not.toBeVisible();
    await expect.poll(() => output.activeSocketCount()).toBe(0);
    await selectIssueByTitle(page, "Widget rendering broken on Safari");
    const inlineTerminal = page.locator(".sole-embedded-session .terminal-container");
    await expect(inlineTerminal).toBeVisible();
    await expect(inlineTerminal.locator("canvas, .xterm-screen").first()).toBeVisible();
    const inlineMarker = "inline clipboard — marker\u00a0value";
    await enableTmuxMouseAndRenderMarker(page, inlineTerminal, output, inlineMarker);
    await dragTerminalCells(page, inlineTerminal, inlineMarker.length);
    if (browserName === "firefox") {
      await expect.poll(() => fallbackWrites.some((write) => write.includes(inlineMarker))).toBe(true);
    } else {
      await expect.poll(() => readBrowserClipboard(page)).toContain(inlineMarker);
    }
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});

test("tmux drag-copy autoscrolls beyond the terminal edge", async ({ page, browserName }) => {
  test.skip(!hasCommand("git") || !hasCommand("tmux", ["-V"]), "git and tmux are required for the real workspace flow");
  let fallbackWrites: string[] = [];
  if (browserName === "chromium") {
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  } else {
    fallbackWrites = await interceptDeniedBrowserClipboard(page);
  }

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServer();
    api = await playwrightRequest.newContext({
      baseURL: isolatedServer.info.base_url,
    });
    const workspace = await createIssueWorkspace(api, 10);
    const output = observeTerminalOutput(page);

    await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
    const terminal = await openTerminalPanel(page);
    await terminal.click({ position: { x: 10, y: 10 } });
    await runAttachedTmuxCommand(page, "set-option -g mouse on");
    const dimensions = await renderScrollMarkers(page, terminal, output);
    if (browserName === "chromium") {
      await setBrowserClipboard(page, "");
    }
    await dragTerminalPastTop(page, terminal, dimensions);
    await expect.poll(() => output.inputIncludes("\x1b[<64;")).toBe(true);
    await expect.poll(() => output.inputIncludes("\x1b[<0;1;1m")).toBe(true);
    await expect.poll(() => output.includes("\x1b]52;"), { timeout: 10_000 }).toBe(true);
    if (browserName === "firefox") {
      await expect.poll(() => fallbackWrites.some((write) => write.includes("edge-scroll-001"))).toBe(true);
    } else {
      await expect.poll(() => readBrowserClipboard(page)).toContain("edge-scroll-001");
    }
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});
