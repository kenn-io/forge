import { expect, test } from "@playwright/test";

import { mockApi } from "../e2e/support/mockApi";

test("reloads stale assets before rendering a Mermaid diagram", async ({ page }) => {
  await mockApi(page);
  let mainFrameNavigations = 0;
  let sequenceChunkRequests = 0;
  page.on("framenavigated", (frame) => {
    if (frame === page.mainFrame()) mainFrameNavigations += 1;
  });
  await page.route(/\/assets\/sequenceDiagram-[^/]+\.js$/, async (route) => {
    sequenceChunkRequests += 1;
    if (sequenceChunkRequests === 1) {
      await route.fulfill({ status: 404, contentType: "text/plain", body: "missing stale chunk" });
      return;
    }
    await route.fallback();
  });

  await page.goto("/pulls/github/acme/widgets/42");
  await page.locator(".edit-body-btn").click();
  await page
    .locator(".body-edit-textarea")
    .fill(
      [
        "```mermaid",
        "sequenceDiagram",
        "  participant Client",
        "  participant Server",
        "  Client->>Server: Send request",
        "  Server-->>Client: Return response",
        "```",
        "",
        "```ts",
        'const ordinary = "code";',
        "```",
      ].join("\n"),
    );
  await page.locator(".body-edit .title-edit-save").click();

  await expect.poll(() => mainFrameNavigations).toBeGreaterThanOrEqual(2);
  await expect.poll(() => sequenceChunkRequests).toBeGreaterThanOrEqual(2);
  await expect(page.locator(".markdown-body .kit-mermaid-viewer__pan svg")).toBeVisible();
  await expect(page.locator(".markdown-body pre.shiki")).toContainText('const ordinary = "code";');
});
