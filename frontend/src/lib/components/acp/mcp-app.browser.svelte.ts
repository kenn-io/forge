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
