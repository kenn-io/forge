// @vitest-environment node
import { createServer as createHttpServer } from "node:http";
import { createServer } from "vite";
import { expect, it, vi } from "vite-plus/test";
import { apiFailureLogging } from "./apiFailureLogging";

it.each([false, true])("captures failed proxy responses only when enabled=%s", async (enabled) => {
  const log = vi.spyOn(console, "error").mockImplementation(() => {});
  const body = "upstream unavailable\n".repeat(4000);
  const upstream = createHttpServer((request, response) => {
    response.writeHead(request.url === "/api/ok" ? 200 : 502, { "Content-Type": "text/plain" });
    response.end(body);
  });
  await new Promise<void>((resolve) => upstream.listen(0, "127.0.0.1", resolve));
  const address = upstream.address();
  if (!address || typeof address === "string") throw new Error("expected TCP address");
  const vite = await createServer({
    configFile: false,
    server: {
      host: "127.0.0.1",
      port: 0,
      proxy: {
        "/api": {
          target: `http://127.0.0.1:${address.port}`,
          configure: apiFailureLogging(enabled),
        },
      },
    },
  });
  try {
    await vite.listen();
    const viteAddress = vite.httpServer?.address();
    if (!viteAddress || typeof viteAddress === "string") throw new Error("expected Vite TCP address");
    const base = `http://127.0.0.1:${viteAddress.port}`;
    for (const route of ["/api/ok", "/api/workspaces/42/refresh?private=value"]) {
      const response = await fetch(base + route, { method: "POST" });
      expect(await response.text()).toBe(body);
    }
    if (enabled) {
      expect(log).toHaveBeenCalledTimes(1);
      expect(JSON.parse(log.mock.calls[0]![0])).toMatchObject({
        event: "dev.api_failure",
        method: "POST",
        path: "/api/workspaces/42/refresh",
        status: 502,
        contentType: "text/plain",
        body: body.slice(0, 65536),
        bodyBytes: Buffer.byteLength(body),
        truncated: true,
        complete: true,
      });
    } else {
      expect(log).not.toHaveBeenCalled();
    }
  } finally {
    await vite.close();
    await new Promise<void>((resolve, reject) => upstream.close((error) => (error ? reject(error) : resolve())));
    log.mockRestore();
  }
});
