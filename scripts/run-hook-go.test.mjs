import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import test from "node:test";

const script = fileURLToPath(new URL("./run-hook-go.sh", import.meta.url));

function run(env = {}) {
  const childEnv = { ...process.env };
  delete childEnv.GOMAXPROCS;
  delete childEnv.GO_TEST_P;
  delete childEnv.KENN_FORGE_HOOK_GO_CONCURRENCY;
  Object.assign(childEnv, env);

  return spawnSync(script, ["sh", "-c", "printf '%s|%s' \"$GOMAXPROCS\" \"$GO_TEST_P\""], {
    encoding: "utf8",
    env: childEnv,
  });
}

function assertSucceeded(result) {
  assert.equal(result.status, 0, result.error?.message ?? result.stderr ?? "hook runner failed");
}

test("Go hooks default to four-way concurrency", () => {
  const result = run();

  assertSucceeded(result);
  assert.equal(result.stdout, "4|4");
});

test("Go hook concurrency is configurable", () => {
  const result = run({ KENN_FORGE_HOOK_GO_CONCURRENCY: "2" });

  assertSucceeded(result);
  assert.equal(result.stdout, "2|2");
});

test("Go hooks preserve explicit tool-specific limits", () => {
  const result = run({
    GOMAXPROCS: "3",
    GO_TEST_P: "5",
    KENN_FORGE_HOOK_GO_CONCURRENCY: "2",
  });

  assertSucceeded(result);
  assert.equal(result.stdout, "3|5");
});

test("Go hooks preserve an explicitly empty package limit", () => {
  const result = run({ GO_TEST_P: "" });

  assertSucceeded(result);
  assert.equal(result.stdout, "4|");
});
