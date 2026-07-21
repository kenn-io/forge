import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataProjectSummary, KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
import type { KataWorkspaceMetadata } from "../../api/kata/workspaces.js";
import KataWorkspaceSidebarPane from "./KataWorkspaceSidebarPane.svelte";

const { showFlash } = vi.hoisted(() => ({ showFlash: vi.fn() }));

vi.mock("@middleman/ui/stores/flash", () => ({ showFlash }));

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

    if (url.pathname === "/api/v1/kata/tasks/snapshot") {
      return response({
        server_instance_id: "server-1",
        daemon_id: "home",
        intent: { scope: "global", authority: "all" },
        generation: 1,
        invalidation_epoch: 1,
        event_cursor: 0,
        fetched_at: fetchedAt,
        projects,
        member_issue_uids: ["issue-1"],
        issues: [issue()],
        enrichment: {
          selected_issue_uid: "issue-1",
          selected_detail: {
            detail: detail(),
            etag: '"rev-1"',
            workspace_target: { available: false },
          },
          selected_history: [],
        },
      });
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
    showFlash.mockReset();
    vi.unstubAllGlobals();
  });

  it("loads embedded task detail through selected snapshot enrichment", async () => {
    const fetchImpl = vi.fn<typeof fetch>(async (input) => {
      const url = new URL(
        typeof input === "string" ? input : input instanceof URL ? input : input.url,
        window.location.origin,
      );
      if (url.pathname === "/api/v1/kata/tasks/snapshot") {
        expect(url.searchParams.get("selected_issue_uid")).toBe("issue-1");
        return response({
          server_instance_id: "server-1",
          daemon_id: "home",
          intent: { scope: "global", authority: "all" },
          generation: 1,
          invalidation_epoch: 1,
          event_cursor: 0,
          fetched_at: fetchedAt,
          projects,
          member_issue_uids: ["issue-1"],
          issues: [issue()],
          enrichment: {
            selected_issue_uid: "issue-1",
            selected_detail: {
              detail: detail(),
              etag: '"rev-1"',
              workspace_target: { available: false },
            },
            selected_history: [],
          },
        });
      }
      if (url.pathname.endsWith("/api/v1/projects/1/recurrences")) {
        return response({ recurrences: [], fetched_at: fetchedAt });
      }
      if (url.pathname.includes("/kata/proxy/") || url.pathname.endsWith("/events")) {
        throw new Error(`legacy read: ${url.pathname}`);
      }
      return response({ error: { code: "not_found", message: `Unhandled ${url.pathname}` } }, 404);
    });
    vi.stubGlobal("fetch", fetchImpl);

    render(KataWorkspaceSidebarPane, { props: { kata } });

    await screen.findByRole("heading", { name: "Ship the thing" });
    expect(fetchImpl).toHaveBeenCalled();
  });

  it("does not reload a stale selection after a mutation acknowledges against superseded props", async () => {
    const issueA = issue();
    const issueB: KataTaskSummary = {
      ...issue(),
      id: 2,
      uid: "issue-2",
      project_id: 2,
      project_uid: "project-roadmap",
      project_name: "Roadmap",
      short_id: "R-2",
      qualified_id: "Roadmap#R-2",
      title: "Review the thing",
    };
    const kataB: KataWorkspaceMetadata = {
      daemon_id: "work",
      issue_uid: issueB.uid,
      project_uid: issueB.project_uid,
    };
    const snapshotRequests: string[] = [];
    let resolveMove!: (value: Response) => void;
    const moveResponse = new Promise<Response>((resolve) => {
      resolveMove = resolve;
    });
    const snapshotBody = (daemonID: string, selected: KataTaskSummary, generation: number) => ({
      server_instance_id: "server-1",
      daemon_id: daemonID,
      intent: { scope: "global", authority: "all" },
      generation,
      invalidation_epoch: generation,
      event_cursor: 0,
      fetched_at: fetchedAt,
      projects,
      member_issue_uids: [selected.uid],
      issues: [selected],
      enrichment: {
        selected_issue_uid: selected.uid,
        selected_detail: {
          detail: {
            issue: { ...selected, body: selected.body ?? "" },
            comments: [],
            labels: [],
            links: [],
          },
          etag: `"rev-${generation}"`,
          workspace_target: { available: false },
        },
        selected_history: [],
      },
    });
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(
        typeof input === "string" ? input : input instanceof URL ? input : input.url,
        window.location.origin,
      );
      const path = `${url.pathname}${url.search}`;
      if (url.pathname === "/api/v1/kata/tasks/snapshot") {
        const selectedUID = url.searchParams.get("selected_issue_uid") ?? "";
        snapshotRequests.push(selectedUID);
        if (selectedUID === issueB.uid) return response(snapshotBody("work", issueB, 2));
        return response(snapshotBody(snapshotRequests.length === 1 ? "home" : "work", issueA, snapshotRequests.length));
      }
      if (url.pathname.endsWith("/recurrences")) {
        return response({ recurrences: [], fetched_at: fetchedAt });
      }
      if (
        path.endsWith("/api/v1/kata/proxy/api/v1/projects/1/issues/issue-1/actions/move") &&
        init?.method === "POST"
      ) {
        return moveResponse;
      }
      return response({ error: { code: "not_found", message: `Unhandled ${path}` } }, 404);
    });
    vi.stubGlobal("fetch", fetchImpl);

    const { rerender } = render(KataWorkspaceSidebarPane, { props: { kata } });
    await screen.findByRole("heading", { name: issueA.title });
    await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Move to another project" }));
    await fireEvent.click(screen.getByRole("button", { name: /Roadmap/ }));

    await rerender({ kata: kataB });
    await screen.findByRole("heading", { name: issueB.title });

    resolveMove(response({ changed: true }));
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(snapshotRequests).toEqual([issueA.uid, issueB.uid]);
    expect(screen.getByRole("heading", { name: issueB.title })).toBeTruthy();
  });

  it("keeps a failed project move retryable through the embedded workspace", async () => {
    const { fetchImpl, moveAttempts } = createFetchStub();
    vi.stubGlobal("fetch", fetchImpl);
    render(KataWorkspaceSidebarPane, { props: { kata } });

    await screen.findByRole("heading", { name: "Ship the thing" });
    expect(screen.getByRole("button", { name: "Filter links" })).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    await fireEvent.click(screen.getByRole("menuitem", { name: "Move to another project" }));
    await fireEvent.click(screen.getByRole("button", { name: /Roadmap/ }));

    await waitFor(() => {
      expect(showFlash).toHaveBeenCalledWith("Could not move task.", { tone: "danger" });
    });
    expect(screen.getByRole("searchbox", { name: "Find project" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: /Roadmap/ }));
    expect(moveAttempts()).toBe(2);
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Move to another project" })).toBeNull();
    });
  });
});
