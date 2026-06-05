import { readFile, writeFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import { startIsolatedE2EServer } from "./support/e2eServer";

test("config file changes update visible repo choices through SSE", async ({ page }) => {
  const server = await startIsolatedE2EServer();
  try {
    await page.goto(`${server.info.base_url}/pulls`);
    await page.locator(".pull-item").first().waitFor({ state: "visible", timeout: 10_000 });

    const selector = page.getByTitle("Select repository");
    await selector.click();
    await expect(
      page.getByRole("option", {
        name: "github.com/hotreload/config-watch",
      }),
    ).toHaveCount(0);

    const initialConfig = await readFile(server.info.config_path, "utf8");
    await writeFile(
      server.info.config_path,
      `${initialConfig}
[[repos]]
owner = "hotreload"
name = "config-watch"
`,
    );

    await expect(
      page.getByRole("option", {
        name: "github.com/hotreload/config-watch",
      }),
    ).toBeVisible({ timeout: 10_000 });
  } finally {
    await server.stop();
  }
});
