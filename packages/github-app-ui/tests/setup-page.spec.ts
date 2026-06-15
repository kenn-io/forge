import { createReadStream, existsSync } from "node:fs";
import type { AddressInfo } from "node:net";
import { createServer, type Server } from "node:http";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";

// These tests stand in for the Go flow server: they serve the built
// dist plus /flow.json and a fake GitHub manifest endpoint, then
// drive the page like a user. They pin the browser half of app
// creation — flow fetch, manifest form POST, redirect into the done
// view — which the Go e2e tests exercise only at the HTTP contract
// level.

const distDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../dist");
const setupPath = "/setup/test-token/";

const contentTypes: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript",
  ".css": "text/css",
  ".svg": "image/svg+xml",
};

interface FakeFlow {
  server: Server;
  base: string;
  manifestPosts: string[];
}

function startFakeFlowServer(opts: { flowGone?: boolean }): Promise<FakeFlow> {
  const manifestPosts: string[] = [];
  const server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", "http://127.0.0.1");
    const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
    if (url.pathname === `${setupPath}flow.json`) {
      if (opts.flowGone) {
        res.writeHead(404).end("no app creation in progress");
        return;
      }
      res.writeHead(200, { "Content-Type": "application/json" }).end(
        JSON.stringify({
          action: `${base}/settings/apps/new?state=test-state`,
          manifest: JSON.stringify({
            name: "middleman-pw",
            redirect_url: `${base}/callback/test-state`,
            hook_attributes: { active: false },
            public: false,
            default_permissions: { contents: "read", pull_requests: "read" },
          }),
          name: "middleman-pw",
          host: "github.com",
        }),
      );
      return;
    }
    if (req.method === "POST" && url.pathname === "/settings/apps/new") {
      let body = "";
      req.on("data", (chunk: Buffer) => {
        body += chunk.toString();
      });
      req.on("end", () => {
        manifestPosts.push(body);
        res.writeHead(302, { Location: `${base}/callback/test-state?code=c&state=test-state` }).end();
      });
      return;
    }
    if (url.pathname.startsWith("/callback/")) {
      res.writeHead(302, { Location: `${setupPath}?step=done` }).end();
      return;
    }
    if (!url.pathname.startsWith(setupPath)) {
      res.writeHead(404).end("not found");
      return;
    }
    const assetPath = url.pathname.slice(setupPath.length);
    const file = path.join(distDir, assetPath === "" ? "index.html" : assetPath);
    if (!file.startsWith(distDir) || !existsSync(file)) {
      res.writeHead(404).end("not found");
      return;
    }
    res.writeHead(200, {
      "Content-Type": contentTypes[path.extname(file)] ?? "application/octet-stream",
    });
    createReadStream(file).pipe(res);
  });
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      resolve({
        server,
        base: `http://127.0.0.1:${(server.address() as AddressInfo).port}${setupPath}`,
        manifestPosts,
      });
    });
  });
}

test("create step renders the flow and hands the manifest to GitHub", async ({ page }) => {
  const fake = await startFakeFlowServer({});
  try {
    await page.goto(fake.base);

    await expect(page.getByRole("heading", { name: "Create the GitHub App for middleman" })).toBeVisible();
    await expect(page.getByText("middleman-pw")).toBeVisible();
    await expect(page.getByText("Disabled — middleman polls")).toBeVisible();
    await expect(page.getByText("pull requests")).toBeVisible();

    // The page auto-continues, but clicking must work too; either way
    // the manifest form POST goes to GitHub and the redirect chain
    // lands on the done view.
    await page.getByRole("button", { name: "Continue to GitHub" }).click();
    await expect(page).toHaveURL(/step=done/);
    await expect(page.getByRole("heading", { name: "App created on GitHub" })).toBeVisible();

    expect(fake.manifestPosts).toHaveLength(1);
    const posted = new URLSearchParams(fake.manifestPosts[0]!);
    const manifest = JSON.parse(posted.get("manifest") ?? "{}") as { name?: string };
    expect(manifest.name).toBe("middleman-pw");
  } finally {
    fake.server.close();
  }
});

test("create step auto-continues without a click", async ({ page }) => {
  const fake = await startFakeFlowServer({});
  try {
    await page.goto(fake.base);
    // No interaction: the countdown submits the manifest by itself.
    await expect(page).toHaveURL(/step=done/, { timeout: 10_000 });
    expect(fake.manifestPosts).toHaveLength(1);
  } finally {
    fake.server.close();
  }
});

test("stale setup link explains the flow is gone", async ({ page }) => {
  const fake = await startFakeFlowServer({ flowGone: true });
  try {
    await page.goto(fake.base);
    await expect(page.getByRole("heading", { name: "This setup link is no longer active" })).toBeVisible();
    expect(fake.manifestPosts).toHaveLength(0);
  } finally {
    fake.server.close();
  }
});
