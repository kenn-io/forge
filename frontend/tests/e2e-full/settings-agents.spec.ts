import { expect, type Page, request as playwrightRequest, test, type APIRequestContext } from "@playwright/test";
import { startIsolatedE2EServer, type IsolatedE2EServer } from "./support/e2eServer";

let isolatedServer: IsolatedE2EServer | undefined;
let api: APIRequestContext | undefined;

test.describe.configure({ mode: "serial" });

test.beforeAll(async () => {
  isolatedServer = await startIsolatedE2EServer();
  api = await playwrightRequest.newContext({
    baseURL: isolatedServer.info.base_url,
  });
});

test.afterAll(async () => {
  await api?.dispose();
  await isolatedServer?.stop();
});

async function expandAgent(page: Page, name: string): Promise<void> {
  await page.getByRole("button", { name: `Edit ${name}` }).click();
}

test("settings preserves quoted empty workspace agent arguments", async ({ page }) => {
  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  await expandAgent(page, "Codex");
  const argsInput = page.getByLabel("Codex arguments");
  const saveButton = page.getByRole("button", {
    name: "Save workspace agents",
  });

  await argsInput.fill('""');
  await expect(saveButton).toBeEnabled();
  const saveResponsePromise = page.waitForResponse(
    (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
  );
  await saveButton.click();
  const saveResponse = await saveResponsePromise;
  const saveBody = await saveResponse.text();
  expect(saveResponse.status(), `PUT /api/v1/settings failed: ${saveBody}`).toBe(200);

  await expect
    .poll(async () => {
      if (!api) {
        throw new Error("settings agents API context not initialized");
      }
      const response = await api.get("/api/v1/settings");
      const settings = (await response.json()) as {
        agents: Array<{ key: string; command: string[] }>;
      };
      return settings.agents.find((agent) => agent.key === "codex")?.command;
    })
    .toEqual(["codex", ""]);

  await page.reload();
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });
  await expandAgent(page, "Codex");
  await expect(page.getByLabel("Codex arguments")).toHaveValue('""');
});

test("settings preserves explicit default built-in agents during other saves", async ({ page }) => {
  if (!api) {
    throw new Error("settings agents API context not initialized");
  }
  const apiContext = api;
  const seedResponse = await apiContext.put("/api/v1/settings", {
    data: {
      agents: [
        {
          key: "codex",
          label: "Codex",
          command: ["codex"],
          enabled: true,
        },
      ],
    },
  });
  const seedBody = await seedResponse.text();
  expect(seedResponse.status(), `PUT /api/v1/settings seed failed: ${seedBody}`).toBe(200);

  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  const saveButton = page.getByRole("button", {
    name: "Save workspace agents",
  });
  await expect(page.getByRole("checkbox", { name: "Codex" })).toBeVisible();
  await expect(page.getByLabel("Codex arguments")).toHaveCount(0);
  await expandAgent(page, "Codex");
  await expect(page.getByLabel("Codex arguments")).toHaveValue("");
  await expect(saveButton).toBeDisabled();

  await expandAgent(page, "Claude");
  await page.getByLabel("Claude arguments").fill("--permission-mode acceptEdits");
  await expect(saveButton).toBeEnabled();
  const saveResponsePromise = page.waitForResponse(
    (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
  );
  await saveButton.click();
  const saveResponse = await saveResponsePromise;
  const saveBody = await saveResponse.text();
  expect(saveResponse.status(), `PUT /api/v1/settings failed: ${saveBody}`).toBe(200);

  await expect
    .poll(async () => {
      const response = await apiContext.get("/api/v1/settings");
      const settings = (await response.json()) as {
        agents: Array<{ key: string; command: string[] }>;
      };
      return {
        codex: settings.agents.find((agent) => agent.key === "codex")?.command,
        claude: settings.agents.find((agent) => agent.key === "claude")?.command,
      };
    })
    .toEqual({
      codex: ["codex"],
      claude: ["claude", "--permission-mode", "acceptEdits"],
    });
});

test("settings preserves disabled built-in agents with empty commands", async ({ page }) => {
  if (!api) {
    throw new Error("settings agents API context not initialized");
  }
  const apiContext = api;
  const seedResponse = await apiContext.put("/api/v1/settings", {
    data: {
      agents: [
        {
          key: "codex",
          label: "Codex",
          command: [],
          enabled: false,
        },
      ],
    },
  });
  const seedBody = await seedResponse.text();
  expect(seedResponse.status(), `PUT /api/v1/settings seed failed: ${seedBody}`).toBe(200);

  await page.goto(`${isolatedServer!.info.base_url}/settings`);
  await page.locator(".settings-page").waitFor({ state: "visible", timeout: 10_000 });

  const saveButton = page.getByRole("button", {
    name: "Save workspace agents",
  });
  await expandAgent(page, "Codex");
  await expect(page.getByLabel("Codex binary")).toHaveValue("");
  await expect(saveButton).toBeDisabled();

  await expandAgent(page, "Claude");
  await page.getByLabel("Claude arguments").fill("--permission-mode acceptEdits");
  await expect(saveButton).toBeEnabled();
  const saveResponsePromise = page.waitForResponse(
    (response) => response.url().endsWith("/api/v1/settings") && response.request().method() === "PUT",
  );
  await saveButton.click();
  const saveResponse = await saveResponsePromise;
  const saveBody = await saveResponse.text();
  expect(saveResponse.status(), `PUT /api/v1/settings failed: ${saveBody}`).toBe(200);

  await expect
    .poll(async () => {
      const response = await apiContext.get("/api/v1/settings");
      const settings = (await response.json()) as {
        agents: Array<{
          key: string;
          command: string[] | null;
          enabled: boolean;
        }>;
      };
      const codex = settings.agents.find((agent) => agent.key === "codex");
      return {
        codex: codex && {
          key: codex.key,
          command: codex.command ?? [],
          enabled: codex.enabled,
        },
        claude: settings.agents.find((agent) => agent.key === "claude")?.command,
      };
    })
    .toEqual({
      codex: {
        key: "codex",
        command: [],
        enabled: false,
      },
      claude: ["claude", "--permission-mode", "acceptEdits"],
    });
});
