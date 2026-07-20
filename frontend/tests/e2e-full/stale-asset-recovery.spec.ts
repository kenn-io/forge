import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";

import { mockApi } from "../e2e/support/mockApi";

function distAssetUrl(assetUrl: string): URL {
  const pathname = new URL(assetUrl).pathname.replace(/^\//, "");
  return new URL(`../../dist/${pathname}`, import.meta.url);
}

test("reloads an outdated frontend before rendering a Mermaid diagram", async ({ page }) => {
  test.setTimeout(45_000);
  await mockApi(page);
  const updatedEntrypointPath = "/assets/index-updated.js";
  const updatedMermaidCorePath = "/assets/mermaid.core-updated.js";
  const updatedSequencePath = "/assets/sequenceDiagram-updated.js";
  let mainFrameNavigations = 0;
  let frontendVersionChecks = 0;
  let oldSequenceRequests = 0;
  let updatedSequenceRequests = 0;
  let currentEntrypointUrl = "";
  let currentMermaidCoreUrl = "";
  let currentSequenceUrl = "";
  page.on("framenavigated", (frame) => {
    if (frame === page.mainFrame()) mainFrameNavigations += 1;
  });
  await page.route(/\/pulls\/github\/acme\/widgets\/42$/, async (route) => {
    const accept = await route.request().headerValue("accept");
    const isVersionCheck = route.request().resourceType() === "fetch" && accept?.includes("text/html");
    const isUpdatedNavigation = route.request().resourceType() === "document" && mainFrameNavigations > 0;
    if (!isVersionCheck && !isUpdatedNavigation) {
      await route.fallback();
      return;
    }

    if (isVersionCheck) frontendVersionChecks += 1;
    const response = await route.fetch();
    const shell = await response.text();
    const entrypointMatch = shell.match(/<script\b[^>]*\btype="module"[^>]*\bsrc="([^"]+)"[^>]*>/);
    const currentEntrypointPath = entrypointMatch?.[1];
    if (!entrypointMatch || !currentEntrypointPath) throw new Error("Frontend entrypoint not found in test shell");

    currentEntrypointUrl ||= new URL(currentEntrypointPath, route.request().url()).href;
    const updatedEntrypoint = entrypointMatch[0].replace(currentEntrypointPath, updatedEntrypointPath);
    await route.fulfill({ response, body: shell.replace(entrypointMatch[0], updatedEntrypoint) });
  });
  await page.route(updatedEntrypointPath, async (route) => {
    if (!currentEntrypointUrl) throw new Error("Current frontend entrypoint URL not captured");

    const body = await readFile(distAssetUrl(currentEntrypointUrl), "utf8");
    const mermaidCoreFilename = body.match(/mermaid\.core-[A-Za-z0-9_-]+\.js/)?.[0];
    if (!mermaidCoreFilename) throw new Error("Mermaid core chunk not found in frontend entrypoint");

    currentMermaidCoreUrl = new URL(mermaidCoreFilename, currentEntrypointUrl).href;
    await route.fulfill({
      status: 200,
      contentType: "application/javascript",
      body: body.replaceAll(mermaidCoreFilename, "mermaid.core-updated.js"),
    });
  });
  await page.route(updatedMermaidCorePath, async (route) => {
    if (!currentMermaidCoreUrl) throw new Error("Current Mermaid core URL not captured");

    const body = await readFile(distAssetUrl(currentMermaidCoreUrl), "utf8");
    const sequenceFilename = body.match(/sequenceDiagram-[A-Za-z0-9_-]+\.js/)?.[0];
    if (!sequenceFilename) throw new Error("Sequence diagram chunk not found in Mermaid core");

    currentSequenceUrl ||= new URL(sequenceFilename, currentMermaidCoreUrl).href;
    await route.fulfill({
      status: 200,
      contentType: "application/javascript",
      body: body.replaceAll(sequenceFilename, "sequenceDiagram-updated.js"),
    });
  });
  await page.route(/\/assets\/sequenceDiagram-[^/?]+\.js$/, async (route) => {
    if (new URL(route.request().url()).pathname === updatedSequencePath) {
      if (!currentSequenceUrl) throw new Error("Current sequence diagram URL not captured");

      updatedSequenceRequests += 1;
      await route.fulfill({
        status: 200,
        contentType: "application/javascript",
        body: await readFile(distAssetUrl(currentSequenceUrl), "utf8"),
      });
      return;
    }

    oldSequenceRequests += 1;
    await route.fulfill({ status: 404, contentType: "text/plain", body: "missing stale chunk" });
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
  await page.waitForLoadState("load");
  await expect.poll(() => frontendVersionChecks).toBeGreaterThanOrEqual(1);
  await expect.poll(() => updatedSequenceRequests).toBeGreaterThanOrEqual(1);
  await expect(page.locator(".markdown-body .kit-mermaid-viewer__pan svg").first()).toBeVisible({ timeout: 20_000 });
  expect(oldSequenceRequests).toBeGreaterThanOrEqual(1);
  await expect(
    page.locator(".markdown-body pre.shiki").filter({ hasText: 'const ordinary = "code";' }).first(),
  ).toBeVisible();
  await expect
    .poll(
      () =>
        page.evaluate(() =>
          Object.keys(window.sessionStorage).filter((key) => key.startsWith("middleman:vite-reload")),
        ),
      { timeout: 10_000 },
    )
    .toEqual([]);
});
