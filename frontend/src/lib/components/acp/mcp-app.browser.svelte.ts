import { Effect } from "effect";
import { expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../../app/runtime.js";
import { decodeAppDescriptor, hostApp } from "./mcp-app.js";
import renderer from "../../../../../internal/mcpserver/app.html?raw";

it("renders generated content through the app bridge and reads tools without accessing the host document", async () => {
  const calls: unknown[] = [];
  const links: string[] = [];
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(input, init);
    if (request.url.endsWith("/mcp-apps/resource")) return Response.json({ html: renderer });
    calls.push(await request.json());
    return Response.json({ content: [{ type: "text", text: "PR #42: passing" }] });
  });
  const frame = document.createElement("iframe");
  frame.setAttribute("sandbox", "allow-scripts allow-same-origin");
  document.body.append(frame);
  const runtime = makeAppRuntime();
  let status = "Loading";
  const execution = runtime.runCommand(
    Effect.scoped(
      hostApp(
        frame,
        {
          title: "Attention",
          html: `<p id="result"></p><script>
      let isolated = false;
      let proxyIsolated = false;
      try { parent.parent.document.body.innerText; } catch { isolated = true; }
      try { parent.document.body.appendChild(parent.document.createElement("script")); } catch { proxyIsolated = true; }
      app.callServerTool({name: "kenn_forge_list_pull_requests", arguments: {isolated, proxyIsolated}}).then(result => {
        document.getElementById("result").textContent = result.content[0].text;
        return app.callServerTool({name: "rendered", arguments: {text: document.getElementById("result").textContent}});
      }).then(() => {
        return app.openLink({url: "https://example.com/pr/42"});
      }).then(() => {
        const anchor = document.createElement("a");
        anchor.href = "https://github.com/acme/widgets/pull/42";
        anchor.innerHTML = "<span>Open pull request</span>";
        document.body.append(anchor);
        anchor.querySelector("span").click();
      });
    </script>`,
        },
        runtime,
        (next) => {
          status = next;
        },
        (url) => {
          links.push(url);
        },
      ),
    ),
    {
      operation: "test MCP app",
      safeContext: {},
      onFailure: (error) => {
        status = String(error);
      },
    },
  );
  try {
    await expect
      .poll(() => calls)
      .toEqual([
        { name: "kenn_forge_list_pull_requests", arguments: { isolated: true, proxyIsolated: true } },
        { name: "rendered", arguments: { text: "PR #42: passing" } },
      ]);
    expect(status).toBe("Ready");
    await expect.poll(() => links).toEqual(["https://example.com/pr/42", "https://github.com/acme/widgets/pull/42"]);
  } finally {
    execution.interrupt();
    await execution.exit;
    await Effect.runPromise(runtime.disposeEffect);
    frame.remove();
    vi.unstubAllGlobals();
  }
});

async function hostTestApp(html: string, onCall: (call: { name: string; arguments: Record<string, unknown> }) => void) {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(input, init);
    if (request.url.endsWith("/mcp-apps/resource")) return Response.json({ html: renderer });
    onCall(await request.json());
    return Response.json({ content: [] });
  });
  const frame = document.createElement("iframe");
  frame.setAttribute("sandbox", "allow-scripts allow-same-origin");
  document.body.append(frame);
  const runtime = makeAppRuntime();
  const state = { status: "Loading" };
  const execution = runtime.runCommand(
    Effect.scoped(
      hostApp(
        frame,
        { title: "Test", html },
        runtime,
        (next) => {
          state.status = next;
        },
        () => {},
      ),
    ),
    {
      operation: "test MCP app",
      safeContext: {},
      onFailure: (error) => {
        state.status = String(error);
      },
    },
  );
  return {
    frame,
    state,
    async [Symbol.asyncDispose]() {
      execution.interrupt();
      await execution.exit;
      await Effect.runPromise(runtime.disposeEffect);
      frame.remove();
      vi.unstubAllGlobals();
    },
  };
}

it("renders the widget again after the chat moves its frame", async () => {
  const calls: unknown[] = [];
  await using app = await hostTestApp(
    `<script>app.callServerTool({name: "rendered", arguments: {}});</script>`,
    (call) => calls.push(call),
  );
  await expect.poll(() => calls.length).toBe(1);
  expect(app.state.status).toBe("Ready");
  // The session pool parks and restores chats by moving their DOM, which
  // replaces the iframe's window.
  const parking = document.createElement("div");
  document.body.append(parking);
  parking.append(app.frame);
  await expect.poll(() => calls.length).toBe(2);
  document.body.append(app.frame);
  await expect.poll(() => calls.length).toBe(3);
  parking.remove();
});

it("lets generated widgets play inline media", async () => {
  const calls: { name: string; arguments: Record<string, unknown> }[] = [];
  await using app = await hostTestApp(
    `<script>
      const violations = [];
      document.addEventListener("securitypolicyviolation", (event) => violations.push(event.effectiveDirective));
      const settle = (element) => new Promise((resolve) => {
        element.addEventListener("loadedmetadata", resolve);
        element.addEventListener("error", resolve);
      });
      const fromData = document.createElement("video");
      fromData.src = "data:video/webm;base64,GkXfo59ChoEB";
      const fromBlob = document.createElement("video");
      fromBlob.src = URL.createObjectURL(new Blob([new Uint8Array([0x1a, 0x45, 0xdf, 0xa3])], {type: "video/webm"}));
      const image = document.createElement("img");
      image.src = URL.createObjectURL(new Blob(["<svg xmlns='http://www.w3.org/2000/svg'/>"], {type: "image/svg+xml"}));
      document.body.append(fromData, fromBlob, image);
      Promise.all([settle(fromData), settle(fromBlob), new Promise((resolve) => { image.onload = resolve; image.onerror = resolve; })])
        .then(() => new Promise((resolve) => setTimeout(resolve, 100)))
        .then(() => app.callServerTool({name: "media", arguments: {violations}}));
    </script>`,
    (call) => calls.push(call),
  );
  await expect.poll(() => calls).toEqual([{ name: "media", arguments: { violations: [] } }]);
  expect(app.state.status).toBe("Ready");
});

it("only promotes valid Forge app descriptors to executable widgets", () => {
  const content = {
    type: "text",
    text: JSON.stringify({ kind: "kenn-forge://apps/generated", title: "Attention", html: "<p>Ready</p>" }),
  };
  expect(decodeAppDescriptor(content)).toEqual({
    kind: "kenn-forge://apps/generated",
    title: "Attention",
    html: "<p>Ready</p>",
  });
  expect(
    decodeAppDescriptor({ ...content, text: JSON.stringify({ title: "Attention", html: "<p>Ready</p>" }) }),
  ).toBeUndefined();
  expect(decodeAppDescriptor({ ...content, text: "invalid" })).toBeUndefined();
});
