import assert from "node:assert/strict";
import { chromium } from "@playwright/test";

const [baseURL] = process.argv.slice(2);
const token = process.env.FORGE_SMOKE_TOKEN;
assert.ok(baseURL && token, "usage: container-browser-smoke.mjs URL (FORGE_SMOKE_TOKEN required)");
const authority = new URL(baseURL).host;
const browser = await chromium.launch({ headless: true });
try {
  const context = await browser.newContext({ extraHTTPHeaders: { "X-Forwarded-Host": authority } });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const response = await page.goto(`${baseURL}/?auth_token=${encodeURIComponent(token)}`);
  assert.equal(response.status(), 200);
  await page.getByRole("heading", { name: "Connect a code forge" }).waitFor({ state: "visible", timeout: 60_000 });
  assert.equal(await page.title(), "kenn-forge");
  assert.deepEqual(errors, []);
  console.log("embedded SPA browser smoke passed");
} finally {
  await browser.close();
}
