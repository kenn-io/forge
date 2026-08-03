import { execFileSync } from "node:child_process";
import { once } from "node:events";
import { chmodSync, mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import os from "node:os";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { startIsolatedE2EServerWithOptions, type IsolatedE2EServer } from "./support/e2eServer";

function git(cwd: string, ...args: string[]): void {
  execFileSync("git", args, { cwd, stdio: "ignore" });
}

async function listen(server: Server): Promise<string> {
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const address = server.address();
  if (!address || typeof address === "string") {
    throw new Error("fake Roborev server did not bind a TCP address");
  }
  return `http://127.0.0.1:${address.port}`;
}

async function close(server: Server): Promise<void> {
  server.close();
  await once(server, "close");
}

test("repository cards show installed Roborev hooks from the process cache", async ({ page }) => {
  const tempDir = mkdtempSync(path.join(os.tmpdir(), "kenn-forge-roborev-repo-"));
  const checkout = path.join(tempDir, "widgets");
  const hooks = path.join(tempDir, "hooks");
  mkdirSync(checkout);
  mkdirSync(hooks);
  git(checkout, "init", "-q");
  git(checkout, "remote", "add", "origin", "https://github.com/acme/widgets.git");
  git(checkout, "config", "core.hooksPath", hooks);

  const hook = path.join(hooks, "post-commit");
  writeFileSync(hook, "#!/bin/sh\n# roborev post-commit hook v4\nroborev post-commit\n");
  chmodSync(hook, 0o755);

  const requests: string[] = [];
  const roborev = createServer((request, response) => {
    requests.push(request.url ?? "");
    response.setHeader("content-type", "application/json");
    if (request.url === "/api/repos") {
      response.end(
        JSON.stringify({
          repos: [
            {
              name: "widgets",
              root_path: checkout,
              identity: "https://github.com/acme/widgets.git",
              count: 0,
            },
            {
              name: "tools",
              root_path: path.join(tempDir, "missing-tools-checkout"),
              identity: "https://github.com/acme/tools.git",
              count: 0,
            },
          ],
          total_count: 2,
        }),
      );
      return;
    }
    if (request.url === "/api/status") {
      response.end(JSON.stringify({ status: "ok" }));
      return;
    }
    response.statusCode = 404;
    response.end(JSON.stringify({ error: "not found" }));
  });

  let forge: IsolatedE2EServer | undefined;
  try {
    const endpoint = await listen(roborev);
    const previousEndpoint = process.env.ROBOREV_ENDPOINT;
    process.env.ROBOREV_ENDPOINT = endpoint;
    try {
      forge = await startIsolatedE2EServerWithOptions({ freshProcess: true });
    } finally {
      if (previousEndpoint === undefined) {
        delete process.env.ROBOREV_ENDPOINT;
      } else {
        process.env.ROBOREV_ENDPOINT = previousEndpoint;
      }
    }

    await page.goto(`${forge.info.base_url}/repos`);
    const widgets = page.locator(".repo-card").filter({
      has: page.getByRole("button", { name: /acme\s*\/\s*widgets/ }),
    });
    const tools = page.locator(".repo-card").filter({
      has: page.getByRole("button", { name: /acme\s*\/\s*tools/ }),
    });

    await expect(widgets.getByRole("img", { name: "Roborev hooks installed" })).toBeVisible();
    await expect(tools.getByRole("img", { name: "Roborev hooks installed" })).toHaveCount(0);

    await page.reload();
    await expect(widgets.getByRole("img", { name: "Roborev hooks installed" })).toBeVisible();
    expect(requests.filter((requestPath) => requestPath === "/api/repos")).toHaveLength(1);
  } finally {
    await forge?.stop();
    await close(roborev);
    rmSync(tempDir, { recursive: true, force: true });
  }
});
