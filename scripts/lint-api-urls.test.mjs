import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { mkdtemp } from "node:fs/promises";

import { lintApiUrls } from "./lint-api-urls.mjs";

async function write(root, file, content) {
  const fullPath = join(root, file);
  await mkdir(dirname(fullPath), { recursive: true });
  await writeFile(fullPath, content);
  return fullPath;
}

async function makeRoot() {
  return mkdtemp("/tmp/middleman-api-lint-");
}

test("flags hardcoded production /api/v1 endpoint calls", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/stores/pulls.svelte.ts",
    ["export async function loadPulls() {", '  return fetch("/api/v1/pulls");', "}", ""].join("\n"),
  );

  const findings = await lintApiUrls({ root });

  assert.equal(findings.length, 1);
  assert.match(findings[0].file, /pulls\.svelte\.ts$/);
  assert.equal(findings[0].line, 2);
  assert.match(findings[0].message, /generated client/);
});

test("ignores tests, generated code, and OpenAPI schema files", async () => {
  const root = await makeRoot();
  await write(root, "frontend/src/lib/api/settings.test.ts", 'expect(url).toBe("/api/v1/settings");\n');
  await write(
    root,
    "frontend/tests/e2e/settings.spec.ts",
    'await page.route("**/api/v1/settings", route => route.fulfill());\n',
  );
  await write(root, "packages/ui/src/api/generated/client.ts", 'export const base = "/api/v1";\n');
  await write(root, "frontend/openapi/openapi.yaml", "servers:\n  - url: /api/v1\n");

  const findings = await lintApiUrls({ root });

  assert.deepEqual(findings, []);
});

test("allows generated client base path helpers", async () => {
  const root = await makeRoot();
  await write(
    root,
    "packages/ui/src/stores/diff.svelte.ts",
    ["function apiBaseURL(basePath) {", '  return `${basePath.replace(/\\/$/, "")}/api/v1`;', "}", ""].join("\n"),
  );

  const findings = await lintApiUrls({ root });

  assert.deepEqual(findings, []);
});

test("allows scoped streaming transports", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/components/terminal/TerminalPane.svelte",
    [
      '<script lang="ts">',
      "  const events = new EventSource(`${basePath}/api/v1/events`);",
      "  const socketUrl =",
      "    `/api/v1/workspaces/${encodeURIComponent(workspaceId)}` +",
      "    `/terminal?cols=${cols}&rows=${rows}`;",
      "  const socket = new WebSocket(socketUrl);",
      "  await fetch(`/api/v1/workspaces/${workspaceId}/logs`, {",
      '    headers: { Accept: "application/x-ndjson" },',
      "  });",
      "</script>",
      "",
    ].join("\n"),
  );

  const findings = await lintApiUrls({ root });

  assert.deepEqual(findings, []);
});
