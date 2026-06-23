import { once } from "node:events";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { expect, test } from "@playwright/test";

function apiURL(baseURL: string, path: string): string {
  return new URL(path, baseURL).toString();
}

type ExternalOrigin = {
  close: () => Promise<void>;
  url: string;
};

async function startExternalOrigin(body = "<!doctype html><title>external origin</title>"): Promise<ExternalOrigin> {
  const server = createServer((_req, res) => {
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    res.end(body);
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const addr = server.address() as AddressInfo;
  return {
    url: `http://127.0.0.1:${addr.port}/`,
    close: () => closeServer(server),
  };
}

async function closeServer(server: Server): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    server.close((err) => {
      if (err) reject(err);
      else resolve();
    });
    // server.close() only stops new connections; it waits forever on
    // keep-alive sockets the browser still holds, which can turn test
    // teardown into a 30s test timeout that masks the real assertion
    // failure. Drop idle connections so close can complete.
    server.closeIdleConnections();
  });
}

test("browser cannot deliver cross-origin JSON mutations (preflight is blocked)", async ({ page, baseURL }) => {
  expect(baseURL).toBeTruthy();
  const target = apiURL(baseURL!, "/api/v1/msgvault/configure");
  const observedResponses: string[] = [];
  page.on("response", (response) => {
    if (response.url() === target) observedResponses.push(response.request().method());
  });
  const externalOrigin = await startExternalOrigin();

  try {
    await page.goto(externalOrigin.url);

    const result = await page.evaluate(async (url) => {
      const controller = new AbortController();
      const timeout = window.setTimeout(() => controller.abort(), 3_000);
      try {
        const response = await fetch(url, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: "{}",
          signal: controller.signal,
        });
        return { ok: true, status: response.status };
      } catch (error) {
        return { ok: false, error: String(error) };
      } finally {
        window.clearTimeout(timeout);
      }
    }, target);

    // A JSON body is not CORS-simple, so the browser preflights the POST.
    // The server grants no CORS approval, the preflight fails, and the
    // POST itself never reaches the wire — so no POST response can exist.
    // Observe responses rather than requests here: page.on("request")
    // reports the browser's *intent* to POST even when the preflight
    // blocks it from being sent.
    expect(result.ok).toBe(false);
    expect(observedResponses).not.toContain("POST");
  } finally {
    await externalOrigin.close();
  }
});

test("browser sends cross-origin simple mutations but cannot read the guarded response", async ({ page, baseURL }) => {
  expect(baseURL).toBeTruthy();
  const target = apiURL(baseURL!, "/api/v1/msgvault/configure");
  const observed: string[] = [];
  page.on("request", (request) => {
    if (request.url() === target) observed.push(request.method());
  });
  const externalOrigin = await startExternalOrigin();

  try {
    await page.goto(externalOrigin.url);

    const result = await page.evaluate(async (url) => {
      const controller = new AbortController();
      const timeout = window.setTimeout(() => controller.abort(), 3_000);
      try {
        const response = await fetch(url, {
          method: "POST",
          headers: { "Content-Type": "text/plain" },
          body: "body=test",
          signal: controller.signal,
        });
        return { ok: true, status: response.status };
      } catch (error) {
        return { ok: false, error: String(error) };
      } finally {
        window.clearTimeout(timeout);
      }
    }, target);

    expect(result.ok).toBe(false);
    // Bound the poll: without a timeout it polls until the test timeout,
    // which buries the real failure under "Test timeout of 30000ms".
    await expect.poll(() => observed, { timeout: 10_000 }).toContain("POST");
    expect(observed).not.toContain("OPTIONS");
  } finally {
    await externalOrigin.close();
  }
});

test("same-origin browser JSON mutations reach the API", async ({ page, baseURL }) => {
  expect(baseURL).toBeTruthy();
  await page.goto(baseURL!);

  const result = await page.evaluate(async () => {
    const response = await fetch("/api/v1/msgvault/configure", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Middleman-Csrf": "1" },
      body: "{}",
    });
    return { status: response.status };
  });

  expect(result.status).toBe(400);
});

test("SPA shell cannot be framed by another origin", async ({ page, baseURL }) => {
  expect(baseURL).toBeTruthy();
  const target = apiURL(baseURL!, "/workspaces");
  const response = await page.request.get(target);
  const headers = response.headers();
  expect(headers["content-security-policy"]).toBe("frame-ancestors 'none'");
  expect(headers["x-frame-options"]).toBe("DENY");

  const consoleErrors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") {
      consoleErrors.push(message.text());
    }
  });

  const escapedTarget = target.replaceAll('"', "&quot;");
  const externalOrigin = await startExternalOrigin(
    `<!doctype html><title>frame attempt</title><iframe id="middleman-frame" src="${escapedTarget}"></iframe>`,
  );

  try {
    await page.goto(externalOrigin.url);

    await expect
      .poll(
        () =>
          consoleErrors.some(
            (text) =>
              text.includes("frame-ancestors") || text.includes("X-Frame-Options") || text.includes("Refused to frame"),
          ),
        { timeout: 10_000 },
      )
      .toBe(true);

    const frameHandle = await page.locator("#middleman-frame").elementHandle();
    expect(frameHandle).not.toBeNull();
    const frame = await frameHandle!.contentFrame();
    expect(await frame?.locator(".workspace-list-sidebar").count()).toBeFalsy();
  } finally {
    await externalOrigin.close();
  }
});

test("workspace embed routes remain frameable", async ({ page, baseURL }) => {
  expect(baseURL).toBeTruthy();
  const target = apiURL(baseURL!, "/workspaces/embed/list");
  const response = await page.request.get(target);
  const headers = response.headers();
  expect(headers["content-security-policy"]).toBeUndefined();
  expect(headers["x-frame-options"]).toBeUndefined();

  const consoleErrors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") {
      consoleErrors.push(message.text());
    }
  });

  const escapedTarget = target.replaceAll('"', "&quot;");
  const externalOrigin = await startExternalOrigin(
    `<!doctype html><title>embed host</title><iframe id="middleman-frame" src="${escapedTarget}"></iframe>`,
  );

  try {
    await page.goto(externalOrigin.url);
    const frameHandle = await page.locator("#middleman-frame").elementHandle();
    expect(frameHandle).not.toBeNull();
    const frame = await frameHandle!.contentFrame();
    expect(frame).not.toBeNull();
    await expect(frame!.locator("body")).toHaveCount(1);
    expect(
      consoleErrors.some(
        (text) =>
          text.includes("frame-ancestors") || text.includes("X-Frame-Options") || text.includes("Refused to frame"),
      ),
    ).toBe(false);
  } finally {
    await externalOrigin.close();
  }
});

test("same-origin browser non-JSON mutations return a JSON 415 error", async ({ page, baseURL }) => {
  expect(baseURL).toBeTruthy();
  await page.goto(baseURL!);

  const result = await page.evaluate(async () => {
    const response = await fetch("/api/v1/msgvault/configure", {
      method: "POST",
      headers: { "Content-Type": "text/plain" },
      body: "body=test",
    });
    return {
      contentType: response.headers.get("Content-Type"),
      status: response.status,
      text: await response.text(),
    };
  });

  expect(result.status).toBe(415);
  expect(result.contentType).toBe("application/json");
  expect(result.text).toContain("Content-Type must be application/json");
});
