import assert from "node:assert/strict";
import { test } from "node:test";

import { runFrontendBuild } from "./build-frontend.mjs";

const transientBuildFailure = [
  "failed to load virtual css module App.svelte?svelte&type=style&lang.css",
  "[lightningcss minify] Invalid empty selector",
].join("\n");

test("retries one transient Svelte virtual CSS build failure", async () => {
  const results = [
    { exitCode: 1, output: transientBuildFailure },
    { exitCode: 0, output: "" },
  ];
  const retryMessages = [];
  let attempts = 0;

  const exitCode = await runFrontendBuild({
    onRetry: (message) => retryMessages.push(message),
    runBuild: async () => results[attempts++],
  });

  assert.equal(exitCode, 0);
  assert.equal(attempts, 2);
  assert.deepEqual(retryMessages, ["Transient Svelte CSS build failure detected; retrying once."]);
});

test("does not retry unrelated build failures", async () => {
  let attempts = 0;

  const exitCode = await runFrontendBuild({
    runBuild: async () => {
      attempts += 1;
      return { exitCode: 1, output: "TypeScript compilation failed" };
    },
  });

  assert.equal(exitCode, 1);
  assert.equal(attempts, 1);
});

test("stops after the retry also fails", async () => {
  let attempts = 0;

  const exitCode = await runFrontendBuild({
    onRetry: () => {},
    runBuild: async () => {
      attempts += 1;
      return { exitCode: 1, output: transientBuildFailure };
    },
  });

  assert.equal(exitCode, 1);
  assert.equal(attempts, 2);
});
