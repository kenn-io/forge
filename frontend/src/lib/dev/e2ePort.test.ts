import { describe, expect, it } from "vite-plus/test";
import net from "node:net";
import { e2eReuseExistingServer, getAvailablePort, parseE2EPort } from "./e2ePort";

describe("mock e2e port helpers", () => {
  it("parses explicit Playwright ports conservatively", () => {
    expect(parseE2EPort("4173")).toBe(4173);
    expect(parseE2EPort("0")).toBeNull();
    expect(parseE2EPort("65536")).toBeNull();
    expect(parseE2EPort("abc")).toBeNull();
    expect(parseE2EPort(undefined)).toBeNull();
  });

  it("only reuses an existing server after explicit opt-in", () => {
    expect(e2eReuseExistingServer({})).toBe(false);
    expect(
      e2eReuseExistingServer({
        PLAYWRIGHT_REUSE_EXISTING_SERVER: "0",
      }),
    ).toBe(false);
    expect(
      e2eReuseExistingServer({
        PLAYWRIGHT_REUSE_EXISTING_SERVER: "true",
      }),
    ).toBe(true);
    expect(
      e2eReuseExistingServer({
        PLAYWRIGHT_REUSE_EXISTING_SERVER: "yes",
      }),
    ).toBe(true);
  });

  it("returns a port that can be bound", async () => {
    const port = await getAvailablePort();

    await new Promise<void>((resolve, reject) => {
      const server = net.createServer();
      server.on("error", reject);
      server.listen(port, "127.0.0.1", () => {
        server.close((err) => {
          if (err) {
            reject(err);
            return;
          }
          resolve();
        });
      });
    });
  });
});
