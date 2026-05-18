import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { readFile } from "node:fs/promises";
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

test("ephemeral environment setup backgrounds dev-ephemeral", async () => {
  const content = await readFile(
    ".codex/environments/environment-ephemeral.toml",
    "utf8",
  );
  const setup = content.match(/\[setup\]\nscript = '''\n(?<script>[\s\S]*?)\n'''/);

  assert.ok(setup?.groups?.script);
  assert.match(setup.groups.script, /nohup make dev-ephemeral >"\$log" 2>&1 &/);
  assert.match(setup.groups.script, /status=tmp\/dev-ephemeral\/dev-ephemeral\.json/);
  assert.doesNotMatch(setup.groups.script, /^make dev-ephemeral$/m);
});
