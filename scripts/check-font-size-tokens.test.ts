import assert from "node:assert/strict";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { test } from "node:test";

import { checkFontSizeTokens } from "./check-font-size-tokens.ts";

async function write(root: string, file: string, content: string): Promise<string> {
  const fullPath = join(root, file);
  await mkdir(dirname(fullPath), { recursive: true });
  await writeFile(fullPath, content);
  return fullPath;
}

async function makeRoot(): Promise<string> {
  return mkdtemp("/tmp/kenn-forge-font-size-token-check-");
}

test("flags raw font-size lengths in frontend styles", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/components/Example.svelte",
    ["<style>", ".title { font-size: 15px; }", "</style>", ""].join("\n"),
  );

  const findings = await checkFontSizeTokens({ root });

  assert.equal(findings.length, 1);
  assert.match(findings[0].file, /Example\.svelte$/);
  assert.equal(findings[0].line, 2);
  assert.match(findings[0].message, /--font-size-\*/);
});

test("flags non-token font-size variables", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/components/Example.svelte",
    ["<style>", ".title { font-size: var(--mobile-type-sm); }", "</style>", ""].join("\n"),
  );

  const findings = await checkFontSizeTokens({ root });

  assert.equal(findings.length, 1);
  assert.match(findings[0].message, /--font-size-\*/);
});

test("flags calc font-size values", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/components/Example.svelte",
    ["<style>", ".title { font-size: calc(var(--font-size-lg) * 1.3); }", "</style>", ""].join("\n"),
  );

  const findings = await checkFontSizeTokens({ root });

  assert.equal(findings.length, 1);
  assert.match(findings[0].message, /calc/);
});

test("allows font-size design tokens and approved relative sizing", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/components/Example.svelte",
    [
      "<style>",
      ".body { font-size: var(--font-size-md); }",
      ".small { font-size: 0.9em; }",
      ".large { font-size: var(--font-size-xl); }",
      ".reset { font-size: inherit; }",
      "</style>",
      "",
    ].join("\n"),
  );

  const findings = await checkFontSizeTokens({ root });

  assert.deepEqual(findings, []);
});

test("allows root font-size token definitions", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/app.css",
    [
      ":root {",
      "  --font-size-md: 13px;",
      "  --font-size-mobile-body: 1.24rem;",
      "}",
      "body { font-size: var(--font-size-root); }",
      "",
    ].join("\n"),
  );

  const findings = await checkFontSizeTokens({ root });

  assert.deepEqual(findings, []);
});

test("flags raw font shorthand sizes", async () => {
  const root = await makeRoot();
  await write(
    root,
    "frontend/src/lib/Button.svelte",
    ["<style>", ".button { font: 600 12px/1.2 var(--font-sans); }", "</style>", ""].join("\n"),
  );

  const findings = await checkFontSizeTokens({ root });

  assert.equal(findings.length, 1);
  assert.equal(findings[0].line, 2);
  assert.match(findings[0].message, /font shorthand/);
});
