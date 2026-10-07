import assert from "node:assert/strict";
import { test } from "node:test";
import { isServerURL, launchURL, readConnection, serverURL } from "./connection.ts";

test("phones always launch the mobile workspace route, including with a server base path", () => {
  assert.equal(
    launchURL({ server: " https://forge.example.test/forge/ ", token: "", desktop: true }, false),
    "https://forge.example.test/forge/m/workspaces",
  );
});

test("desktop is an opt-in tablet layout", () => {
  assert.equal(
    launchURL({ server: "http://10.0.2.2:8080", token: "", desktop: false }, true),
    "http://10.0.2.2:8080/m/workspaces",
  );
  assert.equal(
    launchURL({ server: "http://10.0.2.2:8080", token: "", desktop: true }, true),
    "http://10.0.2.2:8080/terminal?desktop=1",
  );
});

test("launch routes keep auth tokens out of navigation history", () => {
  assert.equal(
    launchURL({ server: "https://forge.example.test", token: " a+b&c ", desktop: false }, false),
    "https://forge.example.test/m/workspaces",
  );
});

test("server input does not accept pasted sign-in links or non-web addresses", () => {
  for (const input of [
    "forge.example.test",
    "file:///tmp/forge",
    "https://user:pass@forge.example.test",
    "https://forge.example.test/?auth_token=secret",
    "https://forge.example.test/#fragment",
  ]) {
    assert.throws(() => serverURL(input));
  }
});

test("restored connections validate fields before use", () => {
  assert.throws(() => readConnection('{"server":"https://forge.example.test","token":42,"desktop":false}'));
  assert.deepEqual(readConnection('{"server":"https://forge.example.test/","token":"","desktop":false}'), {
    server: "https://forge.example.test",
    token: "",
    desktop: false,
  });
});

test("embedded navigation stays on the configured origin", () => {
  assert.equal(isServerURL("https://forge.example.test/forge/m/pulls", "https://forge.example.test/forge"), true);
  assert.equal(isServerURL("https://github.com/acme/widgets", "https://forge.example.test"), false);
  assert.equal(isServerURL("http://forge.example.test/m/workspaces", "https://forge.example.test"), false);
});
