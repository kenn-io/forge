import assert from "node:assert/strict";
import { test } from "node:test";
import { lintDevAuthProxy } from "./check-dev-auth-proxy.mjs";

test("rejects the inline proxy that loses the dev route under a backend base path", () => {
  const findings = lintDevAuthProxy(`const config = { server: { proxy: {
    "^/.*[?&]auth_token=": { target: apiUrl, changeOrigin: true }
  } } };`);
  assert.equal(findings.length, 1);
  assert.match(findings[0], /inline proxies leak backend base_path/);
});

test("accepts the shared base-path-aware proxy, including an import alias", () => {
  assert.deepEqual(
    lintDevAuthProxy(`
    import { authBootstrapProxy as bootstrap } from "./src/lib/dev/authBootstrapProxy.ts";
    const config = { server: { proxy: { "^/.*[?&]auth_token=": bootstrap(apiUrl) } } };
  `),
    [],
  );
});

test("rejects an unrelated function with the same name", () => {
  assert.equal(
    lintDevAuthProxy(`
    function authBootstrapProxy(url) { return { target: url }; }
    const config = { server: { proxy: { "^/.*[?&]auth_token=": authBootstrapProxy(apiUrl) } } };
  `).length,
    1,
  );
});

test("rejects a dev config with no browser login proxy", () => {
  assert.match(lintDevAuthProxy("const config = { server: { proxy: {} } };")[0], /Missing auth_token proxy/);
});
