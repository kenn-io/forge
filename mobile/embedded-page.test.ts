import assert from "node:assert/strict";
import { test } from "node:test";
import { runInNewContext } from "node:vm";
import { gzipSync } from "node:zlib";
import { embeddedPage } from "./embedded-page.ts";

const frontend = '<html><body><div id="app">Bundled Forge</div></body></html>';
const bundle = gzipSync(frontend).toString("base64");

async function boot(token: string, status: number) {
  const page = embeddedPage(bundle, { server: "https://forge.example.test/forge", token, desktop: false }, false);
  const events: unknown[] = [];
  const window = {
    __BASE_PATH__: "",
    ReactNativeWebView: { postMessage: (message: string) => events.push(JSON.parse(message)) },
  };
  await runInNewContext(page.html.match(/<script>([\s\S]*?)<\/script>/)![1], {
    window,
    history: { replaceState: (_state: unknown, _title: string, url: string) => events.push(url) },
    fetch: async (url: string, options: unknown) => {
      events.push({ url, options: structuredClone(options) });
      return { ok: status === 200, status };
    },
    document: { open() {}, write: (html: string) => events.push(html), close() {} },
    atob,
    Uint8Array,
    Blob,
    DecompressionStream,
    Response,
  });
  return { page, events, window };
}

test("bundled frontend mounts after the unauthenticated API probe, with the configured base path", async () => {
  const { page, events, window } = await boot("", 200);
  assert.equal(page.baseUrl, "https://forge.example.test/forge/m/workspaces");
  assert.equal(window.__BASE_PATH__, "/forge/");
  assert.deepEqual(events, [
    page.baseUrl,
    {
      url: "https://forge.example.test/forge/api/v1/settings",
      options: { credentials: "include" },
    },
    frontend,
  ]);
});

test("auth bootstrap encodes a pasted token without inserting it into the page URL", async () => {
  const token = "a+b&c</script>$&";
  const { events, page } = await boot(token, 200);
  const request = events[1] as { url: string };
  assert.equal(new URL(request.url).searchParams.get("auth_token"), token);
  assert.equal(new URL(page.baseUrl).search, "");
  assert.equal(events.at(-1), frontend);
});

test("rejected authentication reports a native error without mounting the frontend", async () => {
  const { events } = await boot("wrong-token", 401);
  assert.deepEqual(events.at(-1), {
    type: "connection-error",
    message: "The server refused this connection. Check your auth token and the server's access settings.",
  });
  assert.equal(events.includes(frontend), false);
});
