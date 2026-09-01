import { execFileSync } from "node:child_process";
import {
  expect,
  request as playwrightRequest,
  test,
  type APIRequestContext,
  type Locator,
  type Page,
} from "@playwright/test";
import { startIsolatedWorkspaceE2EServerWithOptions, type IsolatedE2EServer } from "./support/e2eServer";

type WorkspaceStatusResponse = {
  id: string;
  status: string;
  error_message?: string | null;
};

const terminalOutputTimeoutMs = 15_000;
const workspaceTestTimeoutMs = 120_000;

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

async function createIssueWorkspace(api: APIRequestContext): Promise<WorkspaceStatusResponse> {
  const response = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", { data: {} });
  expect(response.status()).toBe(202);
  const workspace = (await response.json()) as WorkspaceStatusResponse;
  return waitForWorkspaceReady(api, workspace.id);
}

async function openTerminalPanel(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "Open terminal panel" }).click();
  const container = page.locator(".terminal-panel.open .terminal-container");
  await expect(container).toBeVisible();
  await expect(container.locator("canvas, .xterm-screen").first()).toBeVisible();
  return container;
}

function observeTerminalOutput(page: Page): { tail(): string; cursorResult(): string | undefined } {
  const streams: string[] = [];
  page.on("websocket", (socket) => {
    const streamIndex = streams.push("") - 1;
    const decoder = new TextDecoder();
    socket.on("framereceived", ({ payload }) => {
      const chunk = typeof payload === "string" ? payload : decoder.decode(payload, { stream: true });
      streams[streamIndex] = (streams[streamIndex] + chunk).slice(-64 * 1024);
    });
  });
  return {
    tail: () => streams.join("\n").slice(-2_000),
    cursorResult: () =>
      streams
        .map((stream) => stream.match(/KITTY_CURSOR_KEYS_(?:HANDLED|UNEXPECTED_[0-9a-f_]+)/)?.[0])
        .find((result) => result !== undefined),
  };
}

function kittyCursorProbeCommand(): string {
  const script = String.raw`import os,termios,tty
f=0
o=termios.tcgetattr(f)
E=[b"\x1b[A",b"\x1b[1;1:3A",b"\x1b[B",b"\x1b[1;1:3B",b"\x1b[D",b"\x1b[1;1:3D",b"\x1b[C",b"\x1b[1;1:3C"]
def r():
 d=b""
 while len(d)<32 and (not d or d[-1:] not in b"ABCDu"):d+=os.read(f,1)
 return d
R=[]
try:
 tty.setraw(f);os.write(1,b"\x1b[>3u\x1b[?u");q=r()
 if q==b"\x1b[?3u":os.write(1,b"KITTY_CURSOR_PROBE_READY\r\n")
 R=[r() for _ in E]
finally:
 os.write(1,b"\x1b[<u");termios.tcsetattr(f,termios.TCSADRAIN,o)
print("KITTY_CURSOR_KEYS_HANDLED" if q==b"\x1b[?3u" and R==E else "KITTY_CURSOR_KEYS_UNEXPECTED_"+q.hex()+"_"+"_".join(x.hex() for x in R),flush=True)
`;
  const encoded = Buffer.from(script).toString("base64");
  return `python3 -c "import base64;exec(base64.b64decode('${encoded}'))"`;
}

test("Kitty cursor keys reach and are handled by the PTY application", async ({ page }) => {
  test.setTimeout(workspaceTestTimeoutMs);
  test.skip(
    !hasCommand("git") || !hasCommand("python3", ["--version"]),
    "git and python3 are required for the real workspace flow",
  );

  let isolatedServer: IsolatedE2EServer | null = null;
  let api: APIRequestContext | null = null;
  try {
    isolatedServer = await startIsolatedWorkspaceE2EServerWithOptions({ preferPtyOwner: true });
    api = await playwrightRequest.newContext({ baseURL: isolatedServer.info.base_url });
    const workspace = await createIssueWorkspace(api);
    const output = observeTerminalOutput(page);

    await page.goto(`${isolatedServer.info.base_url}/terminal/${workspace.id}`);
    const terminal = await openTerminalPanel(page);
    await expect.poll(() => output.tail(), { timeout: terminalOutputTimeoutMs }).toContain("issue-10");
    await terminal.click({ position: { x: 10, y: 10 } });
    await page.keyboard.insertText(kittyCursorProbeCommand());
    await page.keyboard.press("Enter");
    await expect.poll(() => output.tail(), { timeout: terminalOutputTimeoutMs }).toContain("KITTY_CURSOR_PROBE_READY");

    for (const key of ["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"]) {
      await page.keyboard.down(key);
      await page.keyboard.up(key);
    }

    await expect
      .poll(() => output.cursorResult(), { timeout: terminalOutputTimeoutMs })
      .toBe("KITTY_CURSOR_KEYS_HANDLED");
  } finally {
    await api?.dispose();
    await isolatedServer?.stop();
  }
});
