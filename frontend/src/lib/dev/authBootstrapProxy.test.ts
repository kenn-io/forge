// @vitest-environment node

import { createServer as createHttpServer } from "node:http";
import type { AddressInfo } from "node:net";
import { createServer } from "vite";
import { expect, it } from "vite-plus/test";
import { authBootstrapProxy } from "./authBootstrapProxy";

it.each(["", "/kenn-forge", "/nested/forge/"])(
  "keeps dev login on the requested route with backend base path %s",
  async (basePath) => {
    const backendPaths: string[] = [];
    // Forge handles bootstrap before stripping base_path, and redirects to
    // the request path with auth_token removed (handleAuthBootstrap).
    const backend = createHttpServer((req, res) => {
      const url = new URL(req.url!, "http://localhost");
      backendPaths.push(url.pathname);
      if (url.searchParams.has("auth_token")) {
        url.searchParams.delete("auth_token");
        res.writeHead(303, {
          Location: url.pathname + url.search,
          "Set-Cookie": "kenn_forge_auth=fixture-token; Path=/; HttpOnly; SameSite=Lax",
        });
        res.end();
        return;
      }
      res.writeHead(req.headers.cookie === "kenn_forge_auth=fixture-token" ? 200 : 401);
      res.end();
    });
    await new Promise<void>((resolve) => backend.listen(0, "127.0.0.1", resolve));
    const apiUrl = `http://127.0.0.1:${(backend.address() as AddressInfo).port}${basePath}`;
    const vite = await createServer({
      configFile: false,
      appType: "custom",
      server: {
        host: "127.0.0.1",
        port: 0,
        proxy: {
          "^/.*[?&]auth_token=": authBootstrapProxy(apiUrl),
          "/api": { target: apiUrl, changeOrigin: true },
        },
      },
    });
    vite.middlewares.use((req, res) => {
      res.end(`Vite route: ${req.url}`);
    });
    try {
      await vite.listen();
      const origin = `http://127.0.0.1:${(vite.httpServer!.address() as AddressInfo).port}`;
      const response = await fetch(`${origin}/pulls?view=mine&auth_token=fixture-token`, { redirect: "manual" });
      expect(response.status).toBe(303);
      expect(response.headers.get("location")).toBe("/pulls?view=mine");
      const cookie = response.headers.get("set-cookie")!.split(";")[0];
      expect(cookie).toBe("kenn_forge_auth=fixture-token");
      const page = await fetch(new URL(response.headers.get("location")!, origin));
      expect(await page.text()).toBe("Vite route: /pulls?view=mine");
      const api = await fetch(`${origin}/api/v1/version`, { headers: { Cookie: cookie } });
      expect(api.status).toBe(200);
      expect(backendPaths).toEqual([
        `${basePath.replace(/\/$/, "")}/pulls`,
        `${basePath.replace(/\/$/, "")}/api/v1/version`,
      ]);
    } finally {
      await vite.close();
      await new Promise<void>((resolve, reject) => backend.close((error) => (error ? reject(error) : resolve())));
    }
  },
);
