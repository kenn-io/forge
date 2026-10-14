import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { test } from "node:test";
import { androidVersionCode } from "./app.config.ts";

function expoConfig(version?: string) {
  const env = { ...process.env, FORGE_MOBILE_VERSION: version };
  if (version === undefined) delete env.FORGE_MOBILE_VERSION;
  const output = execFileSync(
    process.execPath,
    [path.join(import.meta.dirname, "node_modules/expo/bin/cli"), "config", "--json", "--type", "public"],
    { cwd: import.meta.dirname, env, encoding: "utf8" },
  );
  return JSON.parse(output);
}

test("release builds carry the Forge release version", () => {
  const config = expoConfig("v0.4.0");
  assert.equal(config.version, "0.4.0");
  assert.equal(config.android.versionCode, 4000);
  assert.equal(config.android.package, "io.kenn.forge.mobile");
});

test("local builds keep the checked-in app version", () => {
  const config = expoConfig();
  assert.equal(config.version, "0.1.0");
  assert.equal(config.android.versionCode, undefined);
});

test("each later release has a higher Android version code", () => {
  const releases = ["0.4.0", "0.4.1", "0.5.0", "0.999.999", "1.0.0", "2100.0.0"];
  const codes = releases.map(androidVersionCode);
  assert.deepEqual(codes, [...codes].sort((a, b) => a - b));
  assert.equal(new Set(codes).size, codes.length);
});

test("versions without a positive Android version code are rejected", () => {
  for (const version of ["0.0.0-20261013120000-0123456789ab", "0.4", "0.1000.0", "next"]) {
    assert.throws(() => androidVersionCode(version), version);
  }
});
