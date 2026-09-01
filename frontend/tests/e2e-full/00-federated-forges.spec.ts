// This lane uses three real TLS origins so browser behavior is exercised from
// both sides of the federation boundary. Keep it early in the suite: starting
// the isolated daemons and opening a real terminal are comparatively expensive.

import { expect, test, type Page } from "@playwright/test";
import { startIsolatedE2EServerWithOptions, type IsolatedE2EServer } from "./support/e2eServer";
import { openSettingsPanel } from "./support/settingsPanel";

test.use({ ignoreHTTPSErrors: true });
test.describe.configure({ mode: "serial", timeout: 120_000 });

let server: IsolatedE2EServer;

test.beforeAll(async () => {
  test.setTimeout(120_000);
  server = await startIsolatedE2EServerWithOptions({ federatedForges: true });
});

test.afterAll(async () => {
  await server?.stop();
});

function federation() {
  const fixture = server.info.federation;
  if (!fixture) throw new Error("federated e2e server did not publish its topology");
  return fixture;
}

async function openAuthenticated(page: Page, origin: string, token: string, route: string): Promise<void> {
  const url = new URL(route, origin);
  url.searchParams.set("auth_token", token);
  await page.goto(url.toString());
  await expect(page).not.toHaveURL(/auth_token=/);
}

function pullRow(page: Page, title: string) {
  return page.getByRole("button", { name: new RegExp(title) }).first();
}

test("projects one provider view with a spoke-local workspace overlay", async ({ page }) => {
  const fixture = federation();

  await openAuthenticated(page, fixture.hub_url, fixture.hub_token, "/pulls");
  const hubPull = pullRow(page, "Add widget caching layer");
  await expect(hubPull).toBeVisible();
  await expect(hubPull.getByLabel("Workspace attached (ready)")).toHaveCount(0);

  await openAuthenticated(page, fixture.spoke_a_url, fixture.spoke_a_token, "/pulls");
  const spokeAPull = pullRow(page, "Add widget caching layer");
  await expect(spokeAPull).toBeVisible();
  await expect(spokeAPull.getByLabel("Workspace attached (ready)")).toBeVisible();

  await openAuthenticated(page, fixture.spoke_b_url, fixture.spoke_b_token, "/pulls");
  await expect(pullRow(page, "Add widget caching layer")).toBeVisible();
  const spokeBPull = pullRow(page, "Fix race condition in event loop");
  await expect(spokeBPull.getByLabel("Workspace attached (ready)")).toBeVisible();
});

test("shows role-aware settings and routes a hub terminal to its owning spoke", async ({ page }) => {
  const fixture = federation();

  await openAuthenticated(page, fixture.hub_url, fixture.hub_token, "/settings");
  await openSettingsPanel(page, "Fleet federation");
  await expect(page.getByText("Federation hub", { exact: true })).toBeVisible();
  await expect(page.getByRole("table", { name: "Federation member status" })).toBeVisible();

  await openAuthenticated(page, fixture.spoke_a_url, fixture.spoke_a_token, "/settings");
  await openSettingsPanel(page, "Fleet federation");
  await expect(page.getByText("Federation spoke", { exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Hub binding" })).toBeVisible();

  await page.goto(new URL("/workspaces", fixture.spoke_a_url).toString());
  const localWorkspace = page.getByRole("button", { name: /Add widget caching layer/ }).first();
  const siblingWorkspace = page.getByRole("button", { name: /Fix race condition in event loop/ }).first();
  await expect(localWorkspace).toBeVisible();
  await expect(localWorkspace).not.toContainText(fixture.spoke_a_node_id);
  await expect(siblingWorkspace).toHaveCount(0);

  // The fixture shares one loopback hostname across ports. Browser cookies do
  // not include the port in their scope, so re-bootstrap whenever the test
  // switches daemons just as a user would open each daemon's tokenized URL.
  await openAuthenticated(page, fixture.hub_url, fixture.hub_token, "/workspaces");
  const proxiedWorkspaceResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname ===
      `/api/v1/fleet/hosts/${fixture.spoke_a_node_id}/workspaces/federated-spoke-a-workspace`,
  );
  await page
    .getByRole("button", { name: /Add widget caching layer/ })
    .first()
    .click();
  expect((await proxiedWorkspaceResponse).status()).toBe(200);
  await expect(page).toHaveURL(new RegExp(`/terminal/fleet/${fixture.spoke_a_node_id}/federated-spoke-a-workspace$`));

  const terminalSocket = page.waitForEvent("websocket", {
    predicate: (socket) =>
      new URL(socket.url()).pathname.includes(
        `/fleet/hosts/${fixture.spoke_a_node_id}/workspaces/federated-spoke-a-workspace/`,
      ),
  });
  await page.getByRole("button", { name: "Open terminal panel" }).click();
  await terminalSocket;
  await expect(page.locator(".terminal-panel.open .terminal-container")).toBeVisible();
});

test("keeps local execution visible through a strict hub outage and recovers", async ({ page, request }) => {
  const fixture = federation();
  await openAuthenticated(page, fixture.spoke_a_url, fixture.spoke_a_token, "/pulls");
  await expect(pullRow(page, "Add widget caching layer")).toBeVisible();

  const offline = await request.post(`${fixture.control_url}/hub/offline`);
  expect(offline.status(), await offline.text()).toBe(200);
  await expect(page.getByRole("heading", { name: "Hub unavailable" })).toBeVisible();
  await expect(pullRow(page, "Add widget caching layer")).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Hub unavailable" })).toBeVisible();
  await expect(page.getByText(/local workspaces remain available/i)).toBeVisible();

  await page.goto(new URL("/workspaces", fixture.spoke_a_url).toString());
  // Provider titles are intentionally absent while strict mode is offline;
  // the durable local launch data still identifies an executable workspace.
  await expect(page.getByRole("button", { name: /Workspace ready.*feature\/caching/ }).first()).toBeEnabled();

  const online = await request.post(`${fixture.control_url}/hub/online`);
  expect(online.status(), await online.text()).toBe(200);
  await page.goto(new URL("/pulls", fixture.spoke_a_url).toString());
  await expect(pullRow(page, "Add widget caching layer")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Hub unavailable" })).toHaveCount(0);
});
