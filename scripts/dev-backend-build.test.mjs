import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

test("writes generated backend OpenAPI input before Go client generation", async () => {
  const script = await readFile("scripts/dev-backend-build.sh", "utf8");

  const createBackendSpecDir = script.indexOf('mkdir -p "$(dirname "$backend_spec")"');
  const writeBackendSpec = script.indexOf('write_if_changed "$backend_spec" "$tmp_backend_spec"');
  const generateGoClient = script.indexOf('"$GO_BIN" generate ./internal/apiclient/generated');

  assert.notEqual(createBackendSpecDir, -1);
  assert.notEqual(writeBackendSpec, -1);
  assert.notEqual(generateGoClient, -1);
  assert.ok(createBackendSpecDir < writeBackendSpec);
  assert.ok(writeBackendSpec < generateGoClient);
});
