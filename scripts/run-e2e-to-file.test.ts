import assert from "node:assert/strict";
import { test } from "node:test";

import { planE2ERuns } from "../frontend/scripts/e2e-run-plan.ts";

test("plans isolated default browser project runs", () => {
  assert.deepEqual(planE2ERuns([]), [["--project=chromium"], ["--project=firefox"]]);
});

test("appends non-project args to each isolated browser project run", () => {
  assert.deepEqual(planE2ERuns(["--headed", "tests/e2e-full/workspace-sidebar.spec.ts"]), [
    ["--project=chromium", "--headed", "tests/e2e-full/workspace-sidebar.spec.ts"],
    ["--project=firefox", "--headed", "tests/e2e-full/workspace-sidebar.spec.ts"],
  ]);
});

test("respects explicit project args as a single requested run", () => {
  assert.deepEqual(planE2ERuns(["--project=firefox", "--headed"]), [["--project=firefox", "--headed"]]);
  assert.deepEqual(planE2ERuns(["--project", "chromium", "--headed"]), [["--project", "chromium", "--headed"]]);
});
