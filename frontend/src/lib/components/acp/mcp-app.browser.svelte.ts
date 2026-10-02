import { Effect } from "effect";
import { expect, it, vi } from "vite-plus/test";
import { makeAppRuntime } from "../../app/runtime.js";
import { decodeAppDescriptor, hostApp } from "./mcp-app.js";
import renderer from "../../../../../internal/mcpserver/app.html?raw";

it("renders generated content through the app bridge and reads tools without accessing the host document", async () => {
  const calls: unknown[] = [];
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
      try { parent.parent.document.body.innerText; } catch { isolated = true; }
      app.callServerTool({name: "kenn_forge_list_pull_requests", arguments: {isolated}}).then(result => {
        document.getElementById("result").textContent = result.content[0].text;
        return app.callServerTool({name: "rendered", arguments: {text: document.getElementById("result").textContent}});
      });
    </script>`,
        },
        runtime,
        (next) => {
          status = next;
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
        { name: "kenn_forge_list_pull_requests", arguments: { isolated: true } },
        { name: "rendered", arguments: { text: "PR #42: passing" } },
      ]);
    expect(status).toBe("Ready");
  } finally {
    execution.interrupt();
    await execution.exit;
    await Effect.runPromise(runtime.disposeEffect);
    frame.remove();
    vi.unstubAllGlobals();
  }
});

it("only promotes valid Forge app descriptors to executable widgets", () => {
  const content = {
    type: "resource",
    uri: "kenn-forge://apps/generated",
    mimeType: "application/json",
    text: JSON.stringify({ title: "Attention", html: "<p>Ready</p>" }),
  };
  expect(decodeAppDescriptor(content)).toEqual({ title: "Attention", html: "<p>Ready</p>" });
  expect(decodeAppDescriptor({ ...content, uri: "https://example.com/app" })).toBeUndefined();
  expect(decodeAppDescriptor({ ...content, text: "invalid" })).toBeUndefined();
});
