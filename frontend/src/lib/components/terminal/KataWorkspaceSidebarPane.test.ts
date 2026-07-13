import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataProjectSummary, KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
import type { KataWorkspaceMetadata } from "../../api/kata/workspaces.js";
import KataWorkspaceSidebarPane from "./KataWorkspaceSidebarPane.svelte";

const fetchedAt = "2026-06-01T12:00:00Z";

function project(id: number, uid: string, name: string, role?: string): KataProjectSummary {
  return { id, uid, name, metadata: role ? { role } : {}, open_count: 1 };
}

const projects = [project(1, "project-alpha", "Alpha"), project(2, "project-roadmap", "Roadmap")];

function issue(): KataTaskSummary {
  return {
    id: 1,
    uid: "issue-1",
    project_id: 1,
    project_uid: "project-alpha",
    project_name: "Alpha",
    short_id: "A-1",
    qualified_id: "Alpha#A-1",
    title: "Ship the thing",
    body: "Body",
    status: "open",
    metadata: {},
    revision: 1,
    author: "fixture-user",
    created_at: fetchedAt,
    updated_at: fetchedAt,
  };
}

function detail(): KataTaskDetail {
  return { issue: { ...issue(), body: "Body" }, comments: [], labels: [], links: [], etag: '"rev-1"' };
}

function response(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function createFetchStub() {
  let moveAttempts = 0;
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(
      typeof input === "string" ? input : input instanceof URL ? input : input.url,
      window.location.origin,
    );
    const path = `${url.pathname}${url.search}`;

    if (path.endsWith("/api/v1/kata/proxy/api/v1/instance")) {
      return response({ instance_uid: "instance-1", version: "dev", schema_version: 10 });
    }
    if (path.endsWith("/api/v1/kata/proxy/api/v1/projects?include=stats")) {
      return response({ projects, fetched_at: fetchedAt });
    }
    if (path.endsWith("/api/v1/kata/proxy/api/v1/issues?status=open")) {
      return response({ issues: [issue()], fetched_at: fetchedAt });
    }
    if (path.endsWith("/api/v1/kata/tasks/issue-1")) {
      return response({ detail: detail(), etag: '"rev-1"' });
    }
    if (path.endsWith("/api/v1/kata/proxy/api/v1/events?limit=1000")) {
      return response({ events: [], fetched_at: fetchedAt });
    }
    if (path.endsWith("/api/v1/kata/proxy/api/v1/projects/1/recurrences")) {
      return response({ recurrences: [], fetched_at: fetchedAt });
    }
    if (path.endsWith("/api/v1/kata/proxy/api/v1/projects/1/issues/issue-1/actions/move") && init?.method === "POST") {
      moveAttempts += 1;
      if (moveAttempts === 1) {
        return response({ error: { code: "move_failed", message: "Could not move task." } }, 409);
      }
      return response({ changed: true, issue: { ...issue(), project_id: 2, project_uid: "project-roadmap" } });
    }

    return response({ error: { code: "not_found", message: `Unhandled ${path}` } }, 404);
  });
  return { fetchImpl, moveAttempts: () => moveAttempts };
}

const kata: KataWorkspaceMetadata = {
  daemon_id: "home",
  issue_uid: "issue-1",
  project_uid: "project-alpha",
};

describe("KataWorkspaceSidebarPane", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("keeps a failed project move retryable through the embedded workspace", async () => {
    const { fetchImpl, moveAttempts } = createFetchStub();
    vi.stubGlobal("fetch", fetchImpl);
    render(KataWorkspaceSidebarPane, { props: { kata } });

    await screen.findByRole("heading", { name: "Ship the thing" });
    await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Move to another project" }));
    await fireEvent.click(screen.getByRole("button", { name: /Roadmap/ }));

    expect((await screen.findByRole("alert")).textContent).toContain("Could not move task.");
    expect(screen.getByRole("searchbox", { name: "Find project" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: /Roadmap/ }));
    expect(moveAttempts()).toBe(2);
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Move to another project" })).toBeNull();
    });
  });
});
