import path from "node:path";
import { expect, request, test } from "@playwright/test";
import { startIsolatedWorkspaceE2EServer } from "./support/e2eServer";

test("the launcher resumes an ACP agent's earlier session in a new chat", async ({ page }, testInfo) => {
  test.setTimeout(120_000);
  const server = await startIsolatedWorkspaceE2EServer();
  const api = await request.newContext({ baseURL: server.info.base_url });
  try {
    const saved = await api.put("/api/v1/settings", {
      data: {
        agents: [
          {
            key: "resumable-chat",
            label: "Resumable chat",
            protocol: "acp",
            enabled: true,
            command: [process.execPath, path.resolve("tests/fixtures/acp-agent.mjs"), "--resumable"],
          },
        ],
      },
    });
    expect(saved.status(), await saved.text()).toBe(200);
    const created = await api.post("/api/v1/issues/github/acme/widgets/10/workspace", { data: {} });
    expect(created.status()).toBe(202);
    const workspace = await created.json();
    await expect
      .poll(async () => (await (await api.get(`/api/v1/workspaces/${workspace.id}`)).json()).status)
      .toBe("ready");

    await page.goto(`${server.info.base_url}/terminal/${workspace.id}`);
    const launcher = page.getByRole("dialog", { name: "Launch a session" });
    await launcher.getByRole("button", { name: "Resume a Resumable chat session" }).click();
    const sessions = launcher.getByRole("group", { name: "Resume a Resumable chat session" });
    const earlier = sessions.getByRole("button", { name: /Earlier parser work/ });
    await expect(earlier).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("acp-resume-list.png") });
    await earlier.click();

    const chat = page.getByRole("region", { name: "Resumable chat chat" });
    const conversation = chat.getByRole("log", { name: "Conversation" });
    await expect(conversation.getByText("Where did we leave off?")).toBeVisible();
    await expect(conversation.getByText("We stopped at the parser tests.")).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("acp-resumed-chat.png") });

    // The chat continues the loaded session rather than a new one.
    await chat.getByRole("textbox", { name: "Message agent" }).fill("Pick up from there");
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    await expect(conversation.getByText("I am working in the", { exact: false })).toBeVisible();

    // A session a chat already holds is not offered again.
    const listed = await api.get(`/api/v1/workspaces/${workspace.id}/runtime/acp-sessions?target_key=resumable-chat`);
    expect(listed.status(), await listed.text()).toBe(200);
    expect((await listed.json()).sessions).toEqual([]);
    const again = await api.post(`/api/v1/workspaces/${workspace.id}/runtime/sessions`, {
      data: { target_key: "resumable-chat", acp_session_id: "earlier-chat" },
    });
    expect(again.status(), await again.text()).toBe(409);
  } finally {
    await api.dispose();
    await server.stop();
  }
});
