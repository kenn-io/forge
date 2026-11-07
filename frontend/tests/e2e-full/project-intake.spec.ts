import { realpathSync } from "node:fs";
import { createGitTestRepository } from "../../../scripts/test-git-fixture.mjs";
import { expect, test } from "@playwright/test";
import type {
  ListProjectsOutputBody,
  ProjectResponse as GeneratedProjectResponse,
} from "../../src/lib/api/generated/models/index.js";

import { startIsolatedE2EServerWithOptions } from "./support/e2eServer";

type ProjectResponse = GeneratedProjectResponse;
type ProjectListResponse = ListProjectsOutputBody;

type SnapshotResponse = {
  hosts: Array<{
    configKey: string;
    kind: string;
    name: string;
  }>;
};

test.describe("project intake", () => {
  test("project intake uses snapshot host metadata and host-scoped registration", async ({ page }) => {
    const server = await startIsolatedE2EServerWithOptions();
    const hostKey = server.info.node_id;
    let cleanupRepository = () => {};
    const repository = createGitTestRepository({
      after: (cleanup: () => void) => {
        cleanupRepository = cleanup;
      },
    });
    const localRepo = realpathSync(repository.root);
    try {
      repository.git("commit", "--allow-empty", "-m", "fixture: seed project");

      const snapshotResponse = await page.request.get(`${server.info.base_url}/api/v1/snapshot?include_peers=true`);
      expect(snapshotResponse.status(), await snapshotResponse.text()).toBe(200);
      const snapshot = (await snapshotResponse.json()) as SnapshotResponse;
      const hubHost = snapshot.hosts.find((host) => host.configKey === hostKey);
      expect(hubHost).toBeDefined();
      expect(hubHost?.kind).toBe("self");

      const snapshotLoaded = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return url.pathname === "/api/v1/snapshot" && url.searchParams.get("include_peers") === "true";
      });
      await page.goto(`${server.info.base_url}/project-intake?host=${encodeURIComponent(hostKey)}`);
      await snapshotLoaded;
      await expect(page.getByText(`Host: ${hubHost?.name ?? "hub"}`)).toBeVisible();

      await page.getByRole("button", { name: /Add an existing repository/ }).click();
      await page.getByLabel("Repository path").fill(localRepo);

      const registerFinished = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return response.request().method() === "POST" && url.pathname === `/api/v1/fleet/hosts/${hostKey}/projects`;
      });
      await page.getByRole("button", { name: "Add repository" }).click();
      const registerResponse = await registerFinished;
      expect(registerResponse.status(), await registerResponse.text()).toBe(201);
      const created = (await registerResponse.json()) as ProjectResponse;
      expect(created.id).not.toBe("");

      await expect(page).toHaveURL(`${server.info.base_url}/`);
      await expect(page.locator(".activity-shell")).toBeVisible();
      const listResponse = await page.request.get(`${server.info.base_url}/api/v1/projects`);
      expect(listResponse.status(), await listResponse.text()).toBe(200);
      const list = (await listResponse.json()) as ProjectListResponse;
      expect(list.projects).toContainEqual(
        expect.objectContaining({
          id: created.id,
          local_path: localRepo,
        }),
      );
    } finally {
      await server.stop();
      cleanupRepository();
    }
  });

  test("accepted project registration survives host replacement without a duplicate", async ({ page }) => {
    const server = await startIsolatedE2EServerWithOptions();
    const hostKey = server.info.node_id;
    let cleanupRepository = () => {};
    const repository = createGitTestRepository({
      after: (cleanup: () => void) => {
        cleanupRepository = cleanup;
      },
    });
    const localRepo = realpathSync(repository.root);
    try {
      repository.git("commit", "--allow-empty", "-m", "fixture: seed project");

      let registrationRequests = 0;
      let noteCommitted: () => void = () => undefined;
      let releaseResponse: () => void = () => undefined;
      const committed = new Promise<void>((resolve) => {
        noteCommitted = resolve;
      });
      const responseGate = new Promise<void>((resolve) => {
        releaseResponse = resolve;
      });
      await page.route(`**/api/v1/fleet/hosts/${hostKey}/projects`, async (route) => {
        if (route.request().method() !== "POST") {
          await route.fallback();
          return;
        }
        registrationRequests += 1;
        const response = await route.fetch();
        noteCommitted();
        await responseGate;
        await route.fulfill({ response });
      });

      await page.goto(`${server.info.base_url}/project-intake?host=${encodeURIComponent(hostKey)}`);
      await page.getByRole("button", { name: /Add an existing repository/ }).click();
      await page.getByLabel("Repository path").fill(localRepo);
      await page.getByRole("button", { name: "Add repository" }).click();
      await committed;

      await page.evaluate(() => {
        history.pushState({}, "", "/project-intake");
        window.dispatchEvent(new PopStateEvent("popstate"));
      });
      await expect(page.getByText("Add an existing local repository")).toBeVisible();
      releaseResponse();
      await expect(page).toHaveURL(/\/project-intake$/);

      await page.evaluate((key) => {
        history.pushState({}, "", `/project-intake?host=${encodeURIComponent(key)}`);
        window.dispatchEvent(new PopStateEvent("popstate"));
      }, hostKey);
      await page.getByRole("button", { name: /Add an existing repository/ }).click();
      await page.getByLabel("Repository path").fill(localRepo);
      await page.getByRole("button", { name: "Add repository" }).click();

      await expect(page).toHaveURL(`${server.info.base_url}/`);
      await expect(page.locator(".activity-shell")).toBeVisible();
      expect(registrationRequests).toBe(1);
      const listResponse = await page.request.get(`${server.info.base_url}/api/v1/projects`);
      expect(listResponse.status(), await listResponse.text()).toBe(200);
      const list = (await listResponse.json()) as ProjectListResponse;
      expect((list.projects ?? []).filter((project) => project.local_path === localRepo)).toHaveLength(1);
    } finally {
      await server.stop();
      cleanupRepository();
    }
  });
});
