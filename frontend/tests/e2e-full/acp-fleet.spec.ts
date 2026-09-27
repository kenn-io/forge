import path from "node:path";
import { expect, request, test } from "@playwright/test";
import { startIsolatedE2EServerWithOptions } from "./support/e2eServer";

test.use({ ignoreHTTPSErrors: true });

test("hub chat exchanges ACP events with the owning spoke", async ({ page }) => {
  test.setTimeout(120_000);
  const server = await startIsolatedE2EServerWithOptions({ federatedForges: true });
  const fleet = server.info.federation;
  if (!fleet) throw new Error("Missing federation fixture");
  const spoke = await request.newContext({
    baseURL: fleet.spoke_a_url,
    ignoreHTTPSErrors: true,
    extraHTTPHeaders: { Authorization: `Bearer ${fleet.spoke_a_token}` },
  });
  const hub = await request.newContext({
    baseURL: fleet.hub_url,
    ignoreHTTPSErrors: true,
    extraHTTPHeaders: { Authorization: `Bearer ${fleet.hub_token}` },
  });
  try {
    const saved = await spoke.put("/api/v1/settings", {
      data: {
        agents: [
          {
            key: "remote-chat",
            label: "Remote chat",
            protocol: "acp",
            enabled: true,
            command: [process.execPath, path.resolve("tests/fixtures/acp-agent.mjs")],
          },
        ],
      },
    });
    expect(saved.status(), await saved.text()).toBe(200);
    const workspaceID = "federated-spoke-a-workspace";
    const route = `/api/v1/fleet/hosts/${fleet.spoke_a_node_id}/workspaces/${workspaceID}`;
    const launched = await hub.post(`${route}/runtime/sessions`, { data: { target_key: "remote-chat" } });
    expect(launched.status(), await launched.text()).toBe(200);
    const session = await launched.json();
    expect(session.kind).toBe("acp");
    const local = await spoke.get(`/api/v1/workspaces/${workspaceID}/runtime`);
    expect((await local.json()).sessions.some((entry: { key: string }) => entry.key === session.key)).toBe(true);

    const url = new URL(`/terminal/fleet/${fleet.spoke_a_node_id}/${workspaceID}`, fleet.hub_url);
    url.searchParams.set("auth_token", fleet.hub_token);
    const socket = page.waitForEvent("websocket", {
      predicate: (connection) =>
        connection.url().includes(`/fleet/hosts/${fleet.spoke_a_node_id}/`) &&
        connection.url().includes("protocol=acp"),
    });
    await page.goto(url.href);
    await page.getByRole("tab", { name: /Remote chat,/ }).click();
    await socket;
    const chat = page.getByRole("region", { name: "Remote chat chat" });
    await chat.getByRole("button", { name: "Model: Fast" }).click();
    await page.getByRole("menuitemradio", { name: "Deep" }).click();
    await expect(chat.getByRole("button", { name: "Model: Deep" })).toBeVisible();
    await chat.getByRole("textbox", { name: "Message agent" }).fill("Inspect this spoke workspace");
    await chat.getByRole("button", { name: "Send", exact: true }).click();
    await expect(chat.getByText("I am working in the", { exact: false })).toBeVisible();
    await chat.getByRole("button", { name: "Allow once" }).click();
    await expect(chat.getByText("Permission received. The turn is complete.")).toBeVisible();
    await page.reload();
    await expect(chat.getByText("Permission received. The turn is complete.")).toBeVisible();
    await expect(chat.getByRole("button", { name: "Model: Deep" })).toBeVisible();
  } finally {
    await spoke.dispose();
    await hub.dispose();
    await server.stop();
  }
});
