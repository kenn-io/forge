// @vitest-environment node

import { createServer as createHttpServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import type { ViteDevServer } from "vite";
import { afterEach, describe, expect, it } from "vite-plus/test";
import { healthcheckPlugin } from "./healthcheckPlugin";

type NextFunction = (err?: unknown) => void;
type Middleware = (req: IncomingMessage, res: ServerResponse, next: NextFunction) => void;

describe("healthcheckPlugin", () => {
  let server: Server | undefined;

  afterEach(async () => {
    const activeServer = server;
    if (!activeServer) return;
    await new Promise<void>((resolve, reject) => {
      activeServer.close((error) => {
        if (error) {
          reject(error);
          return;
        }
        resolve();
      });
    });
    server = undefined;
  });

  async function startServer() {
    let middleware: Middleware | undefined;
    const configureServer = healthcheckPlugin().configureServer;
    if (typeof configureServer !== "function") {
      throw new Error("expected healthcheck plugin to configure the dev server");
    }

    const registerMiddleware = configureServer as (server: ViteDevServer) => void;
    registerMiddleware({
      middlewares: {
        use(handler: Middleware) {
          middleware = handler;
        },
      },
    } as unknown as ViteDevServer);
    const registeredMiddleware = middleware;
    if (!registeredMiddleware) {
      throw new Error("expected healthcheck plugin to register middleware");
    }

    const testServer = createHttpServer((req, res) => {
      registeredMiddleware(req, res, () => {
        res.statusCode = 404;
        res.end();
      });
    });
    server = testServer;
    await new Promise<void>((resolve, reject) => {
      testServer.once("error", reject);
      testServer.listen(0, "127.0.0.1", () => {
        testServer.off("error", reject);
        resolve();
      });
    });

    const address = testServer.address() as AddressInfo | null;
    if (!address) {
      throw new Error("expected healthcheck test server to listen on a TCP address");
    }
    return `http://127.0.0.1:${address.port}`;
  }

  it("serves health endpoints", async () => {
    const baseURL = await startServer();
    for (const path of ["/healthz", "/livez"]) {
      const response = await fetch(baseURL + path);

      expect(response.status).toBe(200);
      await expect(response.json()).resolves.toEqual({ status: "ok" });
    }
  });
});
