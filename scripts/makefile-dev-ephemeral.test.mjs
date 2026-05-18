import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { test } from "node:test";

const execFileAsync = promisify(execFile);

test("dev-ephemeral-stop does not interpolate STATUS into the recipe", async () => {
  const marker = "middleman-status-injection-marker";
  const { stdout } = await execFileAsync("make", [
    "-n",
    "dev-ephemeral-stop",
    `STATUS=x"; touch /tmp/${marker}; #`,
  ]);

  assert.match(stdout, /\$\$STATUS|\$STATUS/);
  assert.doesNotMatch(stdout, new RegExp(marker));
  assert.doesNotMatch(stdout, /touch \/tmp/);
});
