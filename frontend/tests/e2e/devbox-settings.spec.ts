import { expect, test } from "@playwright/test";
import { mockApi, mockSettings } from "./support/mockApi";
import type { Assignment, Connection } from "../../src/lib/api/generated/models";

test("discover, connect and choose a devbox without duplicate rows", async ({ page }, testInfo) => {
  await mockApi(page);
  const machines: Assignment[] = ["Compute A", "Compute B"].map((name, index) => ({
    name,
    host_id: `compute-${index}`,
    account: "developer",
    github_user_id: 123,
    node_id: String(index).repeat(32),
    protocol: 1,
    role: "execution-worker",
    uid: 1000,
    url: `http://compute-${index}.example.ts.net:9100`,
    ssh_address: `developer@compute-${index}.example.ts.net`,
    ssh_host_fingerprint: "SHA256:example",
    maintenance: false,
  }));
  let connections: Connection[] = [];
  let settings = structuredClone(mockSettings);
  await page.route("**/api/v1/settings", async (route) => {
    if (route.request().method() === "PUT")
      settings = { ...settings, workspaces: { ...settings.workspaces, ...route.request().postDataJSON().workspaces } };
    await route.fulfill({ json: settings });
  });
  await page.route("**/api/v1/snapshot**", async (route) => {
    const hosts = connections.map((connection) => ({
      configKey: `devbox:${connection.id}`,
      diagnostics: [],
      id: `devbox:${connection.id}`,
      kind: "devbox",
      name: connection.name,
      federationRole: "devbox",
      operationAvailability: {},
      platform: "linux",
      preferredTransport: "http",
      reachable: true,
      tmuxSessions: [],
    }));
    await route.fulfill({ json: { hosts, workspaces: [] } });
  });
  await page.route("**/api/v1/devboxes**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/discovery")) {
      await route.fulfill({
        json: {
          registry_id: "registry-a",
          registry_url: "https://registry.example.test",
          github_user_id: 123,
          protocol: 1,
          revision: "1",
          devboxes: machines,
        },
      });
    } else if (route.request().method() === "POST") {
      const assignment = machines.find((machine) => machine.host_id === route.request().postDataJSON().host_id)!;
      const connection = { ...assignment, id: assignment.host_id, registry_id: "registry-a", revision: "1" };
      connections = [...connections, connection];
      await route.fulfill({ json: connection });
    } else {
      await route.fulfill({ json: connections });
    }
  });

  await page.goto("/settings");
  await page
    .getByRole("navigation", { name: "Settings" })
    .getByRole("button", { name: /^Workspaces / })
    .click();
  const list = page.getByRole("list", { name: "Workspace machines" });
  await expect(list.getByRole("listitem")).toHaveCount(3);
  await expect(page.getByRole("radio", { name: "Run new workspaces on this Forge machine" })).toBeChecked();
  await expect(page.getByText("https://registry.example.test")).toBeVisible();
  await expect(page.getByLabel("Registry address")).toHaveCount(0);
  await page.getByRole("button", { name: "Connect Compute A", exact: true }).click();
  await expect(page.getByRole("button", { name: "Manage Compute A", exact: true })).toBeVisible();
  await expect(list.getByRole("listitem")).toHaveCount(3);
  await expect(page.getByRole("button", { name: "Connect Compute A", exact: true })).toHaveCount(0);
  await expect(list.getByText("Online", { exact: true })).toBeVisible();
  await page.getByRole("radio", { name: "Run new workspaces on Compute A" }).check();
  await expect(page.getByRole("radio", { name: "Run new workspaces on Compute A" })).toBeChecked();
  await page.reload();
  await page
    .getByRole("navigation", { name: "Settings" })
    .getByRole("button", { name: /^Workspaces / })
    .click();
  await expect(page.getByRole("radio", { name: "Run new workspaces on Compute A" })).toBeChecked();
  await expect(page.getByRole("radio", { name: "Run new workspaces on this Forge machine" })).not.toBeChecked();
  await expect(list.getByRole("listitem")).toHaveCount(3);
  await page.screenshot({ path: testInfo.outputPath("devbox-settings-desktop.png"), fullPage: true });

  await page.setViewportSize({ width: 390, height: 844 });
  const section = page.getByRole("region", { name: "Workspace machines" });
  await section.scrollIntoViewIfNeeded();
  await expect(page.getByRole("button", { name: "Connect Compute B", exact: true })).toBeVisible();
  expect(await section.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  await section.screenshot({ path: testInfo.outputPath("devbox-settings-mobile.png") });
  await page.unrouteAll({ behavior: "wait" });
});
