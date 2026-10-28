import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("./retry-linter-build.sh", import.meta.url));
const streamError = "reading https://sum.golang.org/tile/8/0/x256/257: stream error: stream ID 139; INTERNAL_ERROR; received from peer";

for (const { name, statuses, output, expectedStatus, expectedAttempts, expectedDelays } of [
  { name: "returns a successful build immediately", statuses: [0], output: "built", expectedStatus: 0, expectedAttempts: 1, expectedDelays: "" },
  { name: "recovers after a checksum-service stream failure", statuses: [3, 0], output: streamError, expectedStatus: 0, expectedAttempts: 2, expectedDelays: "5\n" },
  { name: "exhausts three attempts and preserves the final failure status", statuses: [3, 7, 9], output: streamError, expectedStatus: 9, expectedAttempts: 3, expectedDelays: "5\n10\n" },
  { name: "propagates compiler failures immediately", statuses: [17], output: "plugin.go:1: undefined: broken", expectedStatus: 17, expectedAttempts: 1, expectedDelays: "" },
  { name: "propagates checksum mismatches immediately", statuses: [19], output: "verifying module: checksum mismatch", expectedStatus: 19, expectedAttempts: 1, expectedDelays: "" },
  { name: "does not classify another service's stream error as a checksum-service failure", statuses: [23], output: "reading https://example.com/module: stream error: stream ID 139; INTERNAL_ERROR; received from peer", expectedStatus: 23, expectedAttempts: 1, expectedDelays: "" },
]) {
  test(name, (t) => {
    const dir = mkdtempSync(join(tmpdir(), "linter-retry-"));
    t.after(() => rmSync(dir, { recursive: true, force: true }));
    const counter = join(dir, "attempts");
    const delays = join(dir, "delays");
    const builder = join(dir, "builder.mjs");
    writeFileSync(counter, "0");
    writeFileSync(delays, "");
    writeFileSync(builder, `
import { readFileSync, writeFileSync } from "node:fs";
const count = Number(readFileSync(process.env.COUNTER, "utf8"));
writeFileSync(process.env.COUNTER, String(count + 1));
console.error(process.env.BUILD_OUTPUT);
process.exit(JSON.parse(process.env.BUILD_STATUSES)[count] ?? 99);
`);
    writeFileSync(join(dir, "sleep"), '#!/bin/sh\nprintf "%s\\n" "$1" >> "$DELAYS"\n', { mode: 0o700 });

    const result = spawnSync("sh", [script, process.execPath, builder], {
      encoding: "utf8",
      env: {
        ...process.env,
        PATH: `${dir}${delimiter}${process.env.PATH}`,
        COUNTER: counter,
        DELAYS: delays,
        BUILD_OUTPUT: output,
        BUILD_STATUSES: JSON.stringify(statuses),
      },
      timeout: 10_000,
    });

    assert.equal(result.error, undefined);
    assert.equal(result.status, expectedStatus, result.stderr);
    assert.equal(readFileSync(counter, "utf8"), String(expectedAttempts));
    assert.equal(readFileSync(delays, "utf8"), expectedDelays);
    assert.ok((result.stdout + result.stderr).includes(output));
  });
}
