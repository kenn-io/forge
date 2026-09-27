import path from "node:path";
import { devices, expect, request, test } from "@playwright/test";
import { startIsolatedWorkspaceE2EServer } from "./support/e2eServer";
import { openSettingsPanel } from "./support/settingsPanel";

test("ACP workspace streams, approves tools, and reconnects on desktop and phone", async ({
  page,
  browser,
  browserName,
}, testInfo) => {
  test.skip(browserName !== "chromium", "Phone profile uses Chromium");
  test.setTimeout(120_000);
  const server = await startIsolatedWorkspaceE2EServer();
  const api = await request.newContext({ baseURL: server.info.base_url });
  try {
    await page.goto(`${server.info.base_url}/settings`);
    await openSettingsPanel(page, "Workspace agents");
    await page.getByRole("button", { name: "Add custom agent" }).click();
    await page.getByLabel("Custom agent key").fill("chat-fixture");
    await page.getByLabel("Custom agent label").fill("Workspace Chat");
    await page.getByRole("combobox", { name: "Workspace Chat experience: Terminal" }).click();
    await page.getByRole("option", { name: "ACP chat" }).click();
    await page.getByLabel("Workspace Chat binary").fill(process.execPath);
    await page
      .getByLabel("Workspace Chat arguments")
      .fill(JSON.stringify(path.resolve("tests/fixtures/acp-agent.mjs")));
    const save = page.waitForResponse(
      (response) => response.request().method() === "PUT" && response.url().endsWith("/api/v1/settings"),
    );
    await page.getByRole("button", { name: "Save workspace agents" }).click();
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
    await page.goto(`${server.info.base_url}/terminal/${workspace.id}`);
    await page.getByRole("tab", { name: /Workspace Chat,/ }).click();
    const chat = page.getByRole("region", { name: "Workspace Chat chat" });
    await expect(chat).toBeVisible();
    await chat.getByRole("textbox", { name: "Message agent" }).fill("Inspect the workspace");
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    await expect(chat.getByText("I am working in the", { exact: false })).toBeVisible();
    await expect(chat.getByRole("button", { name: "Allow once" })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("acp-desktop.png") });
    await chat.getByRole("button", { name: "Allow once" }).click();
    await expect(chat.getByText("Permission received. The turn is complete.")).toBeVisible();
    await page.reload();
    await expect(chat.getByText("Permission received. The turn is complete.")).toBeVisible();

    const phone = await browser.newContext({ ...devices["iPhone 13"] });
    try {
      const mobile = await phone.newPage();
      await mobile.goto(`${server.info.base_url}/m/workspaces/local/${workspace.id}`);
      // Select the chat session in the phone's single-session workspace view.
      const mobileChat = mobile.getByRole("region", { name: "Workspace Chat chat" });
      if (!(await mobileChat.isVisible())) {
        await mobile.getByRole("combobox", { name: /(?:Terminal|Chat) session:/ }).click();
        await mobile.getByRole("option", { name: /Workspace Chat/ }).click();
      }
      await expect(mobileChat).toBeVisible();
      await expect(mobile.getByRole("button", { name: "Open terminal composer" })).toHaveCount(0);
      await mobileChat.getByRole("textbox", { name: "Message agent" }).fill("Review a change from the phone");
      await mobileChat.getByRole("button", { name: "Send", exact: true }).tap();
      await expect(mobileChat.getByRole("button", { name: "Allow once" })).toBeVisible();
      expect(await mobile.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      const send = await mobileChat.getByRole("button", { name: "Send", exact: true }).boundingBox();
      expect(send?.height).toBeGreaterThanOrEqual(44);
      await mobile.screenshot({ path: testInfo.outputPath("acp-phone.png") });
      await mobileChat.getByRole("button", { name: "Allow once" }).tap();
      await expect(mobileChat.getByRole("button", { name: "Stop reply" })).toHaveCount(0);
    } finally {
      await phone.close();
    }
  } finally {
    await api.dispose();
    await server.stop();
  }
});
