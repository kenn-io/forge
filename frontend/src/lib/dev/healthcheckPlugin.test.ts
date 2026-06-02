// @vitest-environment node

import type { AddressInfo } from "node:net";
import { createServer, mergeConfig, type ViteDevServer } from "vite";
import { afterEach, describe, expect, it } from "vite-plus/test";
import config from "../../../vite.config";

describe("healthcheckPlugin", () => {
  let server: ViteDevServer | undefined;

  afterEach(async () => {
    if (server) {
      await server.close();
      server = undefined;
    }
  });

  async function startServer() {
    server = await createServer({
      ...mergeConfig(config, {
        appType: "custom",
        clearScreen: false,
        configFile: false,
        logLevel: "error",
        server: {
          host: "127.0.0.1",
          port: 0,
        },
      }),
    });

    await server.listen();

    const address = server.httpServer?.address() as AddressInfo | null;
    if (!address) {
      throw new Error("expected Vite test server to listen on a TCP address");
    }

    return `http://127.0.0.1:${address.port}`;
  }

  it.each(["/healthz", "/livez"])("serves %s", async (path) => {
    const baseURL = await startServer();

    const response = await fetch(baseURL + path);

    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toEqual({ status: "ok" });
  });
});
