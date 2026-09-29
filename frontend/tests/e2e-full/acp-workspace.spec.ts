import { createServer, request as httpRequest } from "node:http";
import path from "node:path";
import { devices, expect, request, test } from "@playwright/test";
import { startIsolatedWorkspaceE2EServer } from "./support/e2eServer";
import { openSettingsPanel } from "./support/settingsPanel";

test.use({ launchOptions: { args: ["--host-resolver-rules=MAP acp.test 127.0.0.1"] } });

test("ACP workspace streams, approves tools, and reconnects on desktop and phone", async ({
  page,
  browser,
  browserName,
}, testInfo) => {
  test.skip(browserName !== "chromium", "Phone profile uses Chromium");
  test.setTimeout(120_000);
  const server = await startIsolatedWorkspaceE2EServer();
  const api = await request.newContext({ baseURL: server.info.base_url });
  // A non-localhost HTTP name is insecure even though Chromium resolves it to loopback.
  const upstream = new URL(server.info.base_url);
  const host = "acp.test";
  let origin = "";
  const sockets = new Set<{ destroy(): void }>();
  const proxy = createServer((incoming, outgoing) => {
    const headers = { ...incoming.headers, host: upstream.host };
    if (headers.origin === origin) headers.origin = upstream.origin;
    const forwarded = httpRequest(
      new URL(incoming.url ?? "/", upstream),
      { method: incoming.method, headers },
      (response) => {
        outgoing.writeHead(response.statusCode ?? 502, response.headers);
        response.pipe(outgoing);
      },
    );
    forwarded.on("error", () => {
      outgoing.writeHead(502);
      outgoing.end();
    });
    incoming.pipe(forwarded);
  });
  proxy.on("upgrade", (incoming, client, head) => {
    const headers = { ...incoming.headers, host: upstream.host };
    if (headers.origin === origin) headers.origin = upstream.origin;
    const forwarded = httpRequest(new URL(incoming.url ?? "/", upstream), { headers });
    sockets.add(client);
    client.on("close", () => sockets.delete(client));
    client.on("error", () => forwarded.destroy());
    forwarded.on("error", () => client.destroy());
    forwarded.on("upgrade", (response, peer, peerHead) => {
      sockets.add(peer);
      peer.on("close", () => sockets.delete(peer));
      peer.on("error", () => client.destroy());
      client.on("close", () => peer.destroy());
      peer.on("close", () => client.destroy());
      const responseHeaders = response.rawHeaders.reduce<string[]>((lines, value, index) => {
        if (index % 2 === 0) lines.push(`${value}: ${response.rawHeaders[index + 1]}`);
        return lines;
      }, []);
      client.write(`HTTP/1.1 101 Switching Protocols\r\n${responseHeaders.join("\r\n")}\r\n\r\n`);
      if (head.length) peer.write(head);
      if (peerHead.length) client.write(peerHead);
      client.pipe(peer);
      peer.pipe(client);
    });
    forwarded.end();
  });
  try {
    await new Promise<void>((resolve, reject) => {
      proxy.once("error", reject);
      proxy.listen(0, "127.0.0.1", resolve);
    });
    const address = proxy.address();
    if (!address || typeof address === "string") throw new Error("Missing proxy address");
    origin = `http://${host}:${address.port}`;
    await page.goto(`${server.info.base_url}/settings`);
    await openSettingsPanel(page, "Workspace agents");
    await page.getByRole("button", { name: "Add custom agent" }).click();
    await page.getByLabel("Custom agent key").fill("chat-fixture");
    await page.getByLabel("Custom agent label").fill("Workspace Chat");
    await page.getByRole("combobox", { name: "Workspace Chat experience: Terminal" }).click();
    await page.getByRole("option", { name: "ACP chat" }).click();
    await page.getByLabel("Workspace Chat binary").fill("/usr/local/bin/acp-agent");
    await page.getByLabel("Workspace Chat arguments").fill("--mode review");
    for (const width of [1280, 1024, 600]) {
      await page.setViewportSize({ width, height: 900 });
      const fields = page.locator(".agent-row--custom .field");
      for (const field of await fields.all()) {
        const box = await field.boundingBox();
        const control = await field.locator("input, [role=combobox]").boundingBox();
        if (!box || !control) throw new Error("Agent field is not visible");
        expect(control.x).toBeGreaterThanOrEqual(box.x);
        expect(control.x + control.width).toBeLessThanOrEqual(box.x + box.width + 1);
      }
      await page.screenshot({ path: testInfo.outputPath(`agent-settings-${width}.png`), fullPage: true });
    }
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.getByLabel("Workspace Chat binary").fill(process.execPath);
    await page.getByLabel("Workspace Chat arguments").fill('-e "process.exit(0)"');
    await page.getByRole("button", { name: "Test ACP connection" }).click();
    await expect(page.getByText(/initialize ACP agent:/)).toBeVisible();
    await page
      .getByLabel("Workspace Chat arguments")
      .fill(JSON.stringify(path.resolve("tests/fixtures/acp-agent.mjs")));
    await page.getByRole("button", { name: "Test ACP connection" }).click();
    await expect(page.getByText("ACP connection verified on this host.")).toBeVisible();
    // A command change invalidates the explicit test and is checked again on save.
    await page
      .getByLabel("Workspace Chat arguments")
      .fill(`${JSON.stringify(path.resolve("tests/fixtures/acp-agent.mjs"))} --acp`);
    const automaticCheck = page.waitForResponse((response) => response.url().endsWith("/settings/agents/test-acp"));
    const save = page.waitForResponse(
      (response) => response.request().method() === "PUT" && response.url().endsWith("/api/v1/settings"),
    );
    await page.getByRole("button", { name: "Save workspace agents" }).click();
    expect((await automaticCheck).status()).toBe(200);
    expect((await save).status()).toBe(200);
    const created = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", { data: {} });
    expect(created.status()).toBe(202);
    const workspace = await created.json();
    await expect
      .poll(async () => (await (await api.get(`/api/v1/workspaces/${workspace.id}`)).json()).status)
      .toBe("ready");
    const launched = await api.post(`/api/v1/workspaces/${workspace.id}/runtime/sessions`, {
      data: { target_key: "chat-fixture" },
    });
    expect(launched.status(), await launched.text()).toBe(200);
    const session = await launched.json();
    expect(session.kind).toBe("acp");
    await page.goto(`${origin}/terminal/${workspace.id}`);
    expect(await page.evaluate(() => window.isSecureContext)).toBe(false);
    expect(await page.evaluate(() => typeof crypto.randomUUID)).toBe("undefined");
    await page.getByRole("tab", { name: /Workspace Chat,/ }).click();
    const chat = page.getByRole("region", { name: "Workspace Chat chat" });
    await expect(chat).toBeVisible();
    await chat.getByRole("button", { name: "Model: Fast" }).click();
    await page.getByRole("menuitemradio", { name: "Deep" }).click();
    await expect(chat.getByRole("button", { name: "Model: Deep" })).toBeVisible();
    await chat.getByRole("button", { name: "Effort: Low" }).click();
    await page.getByRole("menuitemradio", { name: "High" }).click();
    await expect(chat.getByRole("button", { name: "Effort: High" })).toBeVisible();
    await chat.getByRole("button", { name: "Agent settings", exact: true }).click();
    await chat.getByRole("combobox", { name: "Mode: Ask" }).click();
    await page.getByRole("option", { name: "Plan", exact: true }).click();
    await expect(chat.getByRole("combobox", { name: "Mode: Plan" })).toBeVisible();
    await chat.getByRole("button", { name: "Agent settings", exact: true }).click();
    await chat.getByRole("textbox", { name: "Message agent" }).fill("Inspect the workspace");
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    await expect(chat.getByText("I am working in the", { exact: false })).toBeVisible();
    await expect(chat.getByRole("button", { name: "Allow once" })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("acp-desktop.png") });
    await chat.getByRole("button", { name: "Allow once" }).click();
    await expect(chat.getByText("Permission received. The turn is complete.")).toBeVisible();
    await page.reload();
    await expect(chat.getByText("Permission received. The turn is complete.")).toBeVisible();
    await chat.getByRole("textbox", { name: "Message agent" }).fill("é".repeat(32769));
    await expect(chat.getByText("Message must not exceed 65,536 bytes.")).toBeVisible();
    await expect(chat.getByRole("button", { name: "Send", exact: true })).toBeDisabled();
    // An accepted 64 KiB prompt exceeds 128 KiB once JSON escapes are included.
    await chat.getByRole("textbox", { name: "Message agent" }).fill('"'.repeat(65536));
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    await expect(chat.getByRole("button", { name: "Allow once" })).toBeVisible();
    await chat.getByRole("button", { name: "Allow once" }).click();
    await expect(chat.getByRole("button", { name: "Stop reply" })).toHaveCount(0);

    const phone = await browser.newContext({ ...devices["iPhone 13"] });
    try {
      const mobile = await phone.newPage();
      await mobile.goto(`${origin}/m/workspaces/local/${workspace.id}`);
      // Select the chat session in the phone's single-session workspace view.
      const mobileChat = mobile.getByRole("region", { name: "Workspace Chat chat" });
      if (!(await mobileChat.isVisible())) {
        await mobile.getByRole("combobox", { name: /(?:Terminal|Chat) session:/ }).click();
        await mobile.getByRole("option", { name: /Workspace Chat/ }).click();
      }
      await expect(mobileChat).toBeVisible();
      await expect(mobileChat.getByRole("button", { name: "Model: Deep" })).toBeVisible();
      await expect(mobileChat.getByRole("button", { name: "Effort: High" })).toBeVisible();
      await expect(mobile.getByRole("button", { name: "Open terminal composer" })).toHaveCount(0);
      await mobileChat.getByRole("textbox", { name: "Message agent" }).fill("Review a change from the phone");
      await mobileChat.getByRole("button", { name: "Send", exact: true }).tap();
      await expect(mobileChat.getByRole("button", { name: "Allow once" })).toBeVisible();
      expect(await mobile.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      // A running turn never locks the composer; its primary action queues instead.
      await expect(mobileChat.getByRole("textbox", { name: "Message agent" })).toBeEnabled();
      const queue = await mobileChat.getByRole("button", { name: "Queue message", exact: true }).boundingBox();
      expect(queue?.height).toBe(40);
      await expect(mobileChat.getByRole("textbox", { name: "Message agent" })).toHaveCSS("font-size", "16px");
      await expect(mobileChat.locator(".message-body").last()).toHaveCSS("font-size", "14px");
      await mobile.screenshot({ path: testInfo.outputPath("acp-phone.png") });
      await mobileChat.getByRole("button", { name: "Allow once" }).tap();
      await expect(mobileChat.getByRole("button", { name: "Stop reply" })).toHaveCount(0);
      await mobile.getByRole("button", { name: "Chat options" }).tap();
      await mobile.getByRole("button", { name: "Stop chat Workspace Chat", exact: true }).tap();
      await expect(mobile.getByText(`Stop chat "Workspace Chat"?`)).toBeVisible();
      await expect(mobile.getByText("This terminates the agent process running in this chat session.")).toBeVisible();
      await mobile.getByRole("button", { name: "Cancel" }).tap();
    } finally {
      await phone.close();
    }
    await chat.getByRole("textbox", { name: "Message agent" }).fill("exit");
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    await expect(page.getByRole("tab", { name: /Workspace Chat,/ })).toHaveCount(0);
  } finally {
    for (const socket of sockets) socket.destroy();
    proxy.closeAllConnections();
    await new Promise<void>((resolve, reject) => proxy.close((error) => (error ? reject(error) : resolve())));
    await api.dispose();
    await server.stop();
  }
});
