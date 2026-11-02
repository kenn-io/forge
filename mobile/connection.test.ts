import assert from "node:assert/strict";
import { test } from "node:test";
import { isForgePageURL, isServerURL, kennLinkPage, launchURL, readConnection, serverURL } from "./connection.ts";

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

test("kenn:// links open every Forge page a kenn:// link can name", () => {
  for (const [link, page] of [
    ["kenn://kenn-forge/worklists/import#v1.abc-_", "/worklists/import#v1.abc-_"],
    ["KENN://kenn-forge/worklists/import#v1.abc", "/worklists/import#v1.abc"],
    ["kenn://kenn-forge", "/"],
    ["kenn://kenn-forge/", "/"],
    ["kenn://kenn-forge/pulls", "/pulls"],
    ["kenn://kenn-forge/pulls/github/acme/api/42", "/pulls/github/acme/api/42"],
    [
      "kenn://kenn-forge/pulls/gitlab/group%2Fsub/api/42/files#comment-7",
      "/pulls/gitlab/group%2Fsub/api/42/files#comment-7",
    ],
    [
      "kenn://kenn-forge/host/git.example.test/issues/forgejo/acme/api/9",
      "/host/git.example.test/issues/forgejo/acme/api/9",
    ],
    ["kenn://kenn-forge/focus/issues/github/acme/api/9", "/focus/issues/github/acme/api/9"],
    ["kenn://kenn-forge/focus/mrs?repo=acme/api", "/focus/mrs?repo=acme/api"],
    [
      "kenn://kenn-forge/repo/browser?provider=github&repo_path=acme/api&ref_type=commit&ref_sha=abc",
      "/repo/browser?provider=github&repo_path=acme/api&ref_type=commit&ref_sha=abc",
    ],
  ]) {
    assert.deepEqual(kennLinkPage(link!), { page }, link);
  }
});

test("kenn:// links that act on load, name another computer, or fall outside the allowlist do not open", () => {
  const unknown = { error: "This version of Forge cannot open this link." };
  for (const link of ["kenn://kenn-forge/settings", "kenn://kenn-forge/terminal/ws-1", "kenn://kenn-forge/m/pulls"]) {
    assert.deepEqual(
      kennLinkPage(link),
      { error: "Forge cannot open this page from a link. Open it in Forge instead." },
      link,
    );
  }
  assert.match(
    (kennLinkPage("kenn://kenn-forge/@0123456789abcdef0123456789abcdef/pulls") as { error: string }).error,
    /another Forge computer/,
  );
  for (const link of [
    "kenn://kenn-forge/worklists/import?auth_token=x#v1.abc",
    "kenn://kenn-forge/pulls?auth_token=x",
    "kenn://kenn-forge/pulls#top",
    "kenn://kenn-forge/pulls/github/acme/api/latest",
    "kenn://kenn-forge/worklists/import/extra#v1.abc",
    "kenn://kenn-forge/pulls/../settings",
    "kenn://kenn-forge/pulls/%2e%2e/settings",
    "kenn://kenn-forge/pulls\\..\\settings",
    "kenn://kenn-forge/pulls /github",
    "kenn://Kenn-Forge/pulls",
    "kenn://kata/kata",
    "kenn://kenn-forge.example/pulls",
    "kenn://user@kenn-forge/pulls",
    "kenn://kenn-forge:80/pulls",
    "https://kenn-forge/pulls",
  ]) {
    assert.deepEqual(kennLinkPage(link), unknown, link);
  }
});
