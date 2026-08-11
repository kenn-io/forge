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
  return mkdtemp("/tmp/kenn-forge-api-lint-");
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

test("flags the removed Kata proxy helper", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/api/kata/daemons.ts",
    ["export function kataProxyPath(path) {", "  return `/api/v1/kata/proxy/${path}`;", "}", ""].join("\n"),
  );

  const findings = await lintApiUrls({ root });

  assert.equal(findings.length, 1);
  assert.equal(findings[0].line, 2);
});

test("flags API prefixes assembled through constants", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/api/provider-routes.ts",
    [
      'const API_PREFIX = "/api" + "/v1";',
      "const MARKDOWN_IMAGE_URL = `${API_PREFIX}/repo/github/acme/widgets/markdown-image`;",
      "export { MARKDOWN_IMAGE_URL };",
      "",
    ].join("\n"),
  );

  const findings = await lintApiUrls({ root });

  assert.equal(findings.length, 1);
  assert.equal(findings[0].line, 1);
  assert.match(findings[0].message, /generated client/);
  assert.match(findings[0].message, /configured API base/);
});

test("flags assembled API prefixes in Svelte script tags with spaced closing syntax", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/components/Example.svelte",
    ["<script>", 'const API_PREFIX = "/api" + "/v1";', "</script >", "<p>Example</p>", ""].join("\n"),
  );

  const findings = await lintApiUrls({ root });

  assert.equal(findings.length, 1);
  assert.equal(findings[0].line, 2);
});

test("ignores tests, generated code, and OpenAPI schema files", async () => {
  const root = await makeRoot();
  await write(root, "frontend/src/lib/api/settings.test.ts", 'expect(url).toBe("/api/v1/settings");\n');
  await write(
    root,
    "frontend/tests/e2e/settings.spec.ts",
    'await page.route("**/api/v1/settings", route => route.fulfill());\n',
  );
  await write(root, "frontend/src/lib/api/generated/client.ts", 'export const base = "/api/v1";\n');
  await write(root, "frontend/openapi/openapi.yaml", "servers:\n  - url: /api/v1\n");

  const findings = await lintApiUrls({ root });

  assert.deepEqual(findings, []);
});

test("allows generated client base path helpers", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/api/runtime-base.ts",
    ["function apiBaseURL(basePath) {", '  return `${basePath.replace(/\\/$/, "")}/api/v1`;', "}", ""].join("\n"),
  );

  const findings = await lintApiUrls({ root });

  assert.deepEqual(findings, []);
});

test("flags dynamic API-base aliases and their endpoint consumers", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/api/manual.ts",
    [
      "const base = `${basePath}/api/v1`;",
      "const settingsURL = `${base}/settings`;",
      "export const loadSettings = () => fetch(settingsURL);",
      "",
    ].join("\n"),
  );

  const findings = await lintApiUrls({ root });

  assert.deepEqual(
    findings.map((finding) => finding.line),
    [1, 2, 3],
  );
  assert.ok(findings.every((finding) => /generated client/.test(finding.message)));
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
