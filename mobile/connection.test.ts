import assert from "node:assert/strict";
import { test } from "node:test";
import { isForgePageURL, isServerURL, launchURL, readConnection, serverURL } from "./connection.ts";

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
    "http://10.0.2.2:8080/workspaces?desktop=1",
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

test("in-app pages stay under the server's base path", () => {
  const server = "https://forge.example.test/forge";
  for (const target of [
    "https://forge.example.test/forge",
    "https://forge.example.test/forge/",
    "https://forge.example.test/forge/m/pulls?state=open#top",
  ]) {
    assert.equal(isForgePageURL(target, server), true, target);
  }
  for (const target of [
    "https://forge.example.test/forgery",
    "https://forge.example.test/reports",
    "https://forge.example.test/",
    "http://forge.example.test/forge/m/pulls",
    "https://other.example.test/forge/m/pulls",
    "not a url",
  ]) {
    assert.equal(isForgePageURL(target, server), false, target);
  }
});

test("a root server keeps every same-origin page in the app", () => {
  assert.equal(isForgePageURL("https://forge.example.test/", "https://forge.example.test"), true);
  assert.equal(isForgePageURL("https://forge.example.test/reports/q3", "https://forge.example.test"), true);
  assert.equal(isForgePageURL("https://other.example.test/reports", "https://forge.example.test"), false);
});
