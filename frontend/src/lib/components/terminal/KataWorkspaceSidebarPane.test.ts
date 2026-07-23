import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";

import type { KataProjectSummary, KataRecurrence, KataTaskDetail, KataTaskSummary } from "../../api/kata/taskTypes.js";
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

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function recurrence(): KataRecurrence {
  return {
    id: 41,
    uid: "recurrence-41",
    project_id: 1,
    rrule: "FREQ=WEEKLY",
    dtstart: "2026-06-01T09:00:00Z",
    timezone: "UTC",
    template_title: "Weekly ship review",
    template_body: "",
    template_labels: [],
    template_metadata: {},
    next_occurrence_key: "2026-06-08T09:00:00Z",
    author: "fixture-user",
    revision: 1,
    created_at: fetchedAt,
    updated_at: fetchedAt,
  };
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

  it("keeps the newest task fenced and revalidates it once after an older mutation acknowledges", async () => {
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
    let commentAttempts = 0;
    const mutationResponse = deferred<Response>();
    const replacementB = deferred<Response>();
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
        if (snapshotRequests.length === 1) return response(snapshotBody("home", issueA, 1));
        if (snapshotRequests.length === 2) return response(snapshotBody("work", issueB, 2));
        if (snapshotRequests.length === 3) return replacementB.promise;
        throw new Error(`unexpected snapshot request ${selectedUID}`);
      }
      if (url.pathname.endsWith("/recurrences")) {
        return response({ recurrences: [], fetched_at: fetchedAt });
      }
      if (url.pathname.endsWith("/comments") && init?.method === "POST") {
        commentAttempts += 1;
        return commentAttempts === 1 ? mutationResponse.promise : response({ changed: true });
      }
      return response({ error: { code: "not_found", message: `Unhandled ${path}` } }, 404);
    });
    vi.stubGlobal("fetch", fetchImpl);

    const { rerender } = render(KataWorkspaceSidebarPane, { props: { kata } });
    await screen.findByRole("heading", { name: issueA.title });
    await fireEvent.input(screen.getByRole("textbox", { name: "Comment" }), {
      target: { value: "Persist on A" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
    await waitFor(() => expect(commentAttempts).toBe(1));

    await rerender({ kata: kataB });
    await screen.findByRole("heading", { name: issueB.title });

    await fireEvent.input(screen.getByRole("textbox", { name: "Comment" }), {
      target: { value: "Must stay fenced on B" },
    });
    expect((screen.getByRole("button", { name: "Add comment" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("button", { name: "More actions" }).matches(":disabled")).toBe(true);
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
    expect(commentAttempts).toBe(1);

    mutationResponse.resolve(response({ changed: true }));
    await waitFor(() => expect(snapshotRequests).toEqual([issueA.uid, issueB.uid, issueB.uid]));
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
    expect(commentAttempts).toBe(1);

    replacementB.resolve(response(snapshotBody("work", issueB, 3)));
    await waitFor(() => expect(screen.queryByText("Change saved. Refreshing Kata snapshot…")).toBeNull());

    expect(commentAttempts).toBe(1);
    expect(screen.getByRole("heading", { name: issueB.title })).toBeTruthy();
    expect((screen.getByRole("textbox", { name: "Comment" }) as HTMLTextAreaElement).value).toBe(
      "Must stay fenced on B",
    );
  });

  it("keeps recurrence mutations fenced until snapshot and recurrence replacement both succeed", async () => {
    let snapshotAttempts = 0;
    let recurrenceReads = 0;
    let deleteAttempts = 0;
    let commentAttempts = 0;
    const successfulRecurrenceRefresh = deferred<Response>();
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(
        typeof input === "string" ? input : input instanceof URL ? input : input.url,
        window.location.origin,
      );
      if (url.pathname === "/api/v1/kata/tasks/snapshot") {
        snapshotAttempts += 1;
        return response({
          server_instance_id: "server-1",
          daemon_id: "home",
          intent: { scope: "global", authority: "all" },
          generation: snapshotAttempts,
          invalidation_epoch: snapshotAttempts,
          event_cursor: 0,
          fetched_at: fetchedAt,
          projects,
          member_issue_uids: [issue().uid],
          issues: [issue()],
          enrichment: {
            selected_issue_uid: issue().uid,
            selected_detail: {
              detail: detail(),
              etag: `"rev-${snapshotAttempts}"`,
              workspace_target: { available: false },
            },
            selected_history: [],
          },
        });
      }
      if (url.pathname.endsWith("/recurrences") && init?.method !== "DELETE") {
        recurrenceReads += 1;
        if (recurrenceReads === 1) return response({ recurrences: [recurrence()], fetched_at: fetchedAt });
        if (recurrenceReads === 2) {
          return response({ error: { code: "recurrence_refresh_failed", message: "Refresh failed." } }, 503);
        }
        return successfulRecurrenceRefresh.promise;
      }
      if (url.pathname.endsWith(`/recurrences/${recurrence().uid}`) && init?.method === "DELETE") {
        deleteAttempts += 1;
        return new Response(null, { status: 204 });
      }
      if (url.pathname.endsWith("/comments") && init?.method === "POST") {
        commentAttempts += 1;
        return response({ changed: true });
      }
      return response({ error: { code: "not_found", message: `Unhandled ${url.pathname}` } }, 404);
    });
    vi.stubGlobal("fetch", fetchImpl);

    render(KataWorkspaceSidebarPane, { props: { kata } });
    await screen.findByRole("button", { name: recurrence().template_title });
    await fireEvent.click(screen.getByRole("button", { name: "Delete recurrence" }));
    await fireEvent.click(
      within(screen.getByRole("dialog", { name: "Delete recurrence" })).getByRole("button", { name: "Delete" }),
    );

    await waitFor(() => expect(deleteAttempts).toBe(1));
    await waitFor(() => expect(snapshotAttempts).toBe(2));
    await waitFor(() => expect(recurrenceReads).toBe(2));
    expect((await screen.findByRole("alert")).textContent).toContain("saved");
    await fireEvent.input(screen.getByRole("textbox", { name: "Comment" }), {
      target: { value: "Must wait for recurrence refresh" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
    expect(commentAttempts).toBe(0);

    await fireEvent.click(screen.getByRole("button", { name: "Retry Kata snapshot" }));
    await waitFor(() => expect(snapshotAttempts).toBe(3));
    await waitFor(() => expect(recurrenceReads).toBe(3));
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
    expect(commentAttempts).toBe(0);

    successfulRecurrenceRefresh.resolve(response({ recurrences: [], fetched_at: fetchedAt }));
    await waitFor(() => expect(screen.queryByText("Change saved. Refreshing Kata snapshot…")).toBeNull());
    expect(deleteAttempts).toBe(1);
  });

  it("retries a conflicted recurrence delete with the reloaded revision", async () => {
    let snapshotAttempts = 0;
    let recurrenceReads = 0;
    const seenIfMatches: string[] = [];
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(
        typeof input === "string" ? input : input instanceof URL ? input : input.url,
        window.location.origin,
      );
      if (url.pathname === "/api/v1/kata/tasks/snapshot") {
        snapshotAttempts += 1;
        return response({
          server_instance_id: "server-1",
          daemon_id: "home",
          intent: { scope: "global", authority: "all" },
          generation: snapshotAttempts,
          invalidation_epoch: snapshotAttempts,
          event_cursor: 0,
          fetched_at: fetchedAt,
          projects,
          member_issue_uids: [issue().uid],
          issues: [issue()],
          enrichment: {
            selected_issue_uid: issue().uid,
            selected_detail: {
              detail: detail(),
              etag: `"rev-${snapshotAttempts}"`,
              workspace_target: { available: false },
            },
            selected_history: [],
          },
        });
      }
      if (url.pathname.endsWith("/recurrences") && init?.method !== "DELETE") {
        recurrenceReads += 1;
        // The recurrence is bumped by another client after the first read, so
        // the post-conflict reload serves a newer revision.
        const revision = recurrenceReads === 1 ? 9 : 10;
        return response({ recurrences: [{ ...recurrence(), revision }], fetched_at: fetchedAt });
      }
      if (url.pathname.endsWith(`/recurrences/${recurrence().uid}`) && init?.method === "DELETE") {
        const ifMatch = new Headers(init?.headers).get("If-Match") ?? "";
        seenIfMatches.push(ifMatch);
        if (ifMatch !== '"rev-10"') {
          return response({ error: { code: "revision_conflict", message: "Recurrence changed." } }, 412);
        }
        return new Response(null, { status: 204 });
      }
      return response({ error: { code: "not_found", message: `Unhandled ${url.pathname}` } }, 404);
    });
    vi.stubGlobal("fetch", fetchImpl);

    render(KataWorkspaceSidebarPane, { props: { kata } });
    await screen.findByRole("button", { name: recurrence().template_title });
    await fireEvent.click(screen.getByRole("button", { name: "Delete recurrence" }));
    const deleteDialog = screen.getByRole("dialog", { name: "Delete recurrence" });
    await fireEvent.click(within(deleteDialog).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(seenIfMatches).toEqual(['"rev-9"']));
    await waitFor(() => expect(recurrenceReads).toBe(2));
    // Let the failed delete settle so the dialog accepts the retry click.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.getByRole("dialog", { name: "Delete recurrence" })).toBeTruthy();

    await fireEvent.click(within(deleteDialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(seenIfMatches).toEqual(['"rev-9"', '"rev-10"']));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Delete recurrence" })).toBeNull());
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

  it("keeps a successful comment acknowledged and retries only its failed snapshot replacement", async () => {
    let snapshotAttempts = 0;
    let commentAttempts = 0;
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(
        typeof input === "string" ? input : input instanceof URL ? input : input.url,
        window.location.origin,
      );
      if (url.pathname === "/api/v1/kata/tasks/snapshot") {
        snapshotAttempts += 1;
        if (snapshotAttempts === 2) {
          return response({ error: { code: "refresh_failed", message: "Snapshot refresh failed." } }, 503);
        }
        return response({
          server_instance_id: "server-1",
          daemon_id: "home",
          intent: { scope: "global", authority: "all" },
          generation: snapshotAttempts,
          invalidation_epoch: snapshotAttempts,
          event_cursor: 0,
          fetched_at: fetchedAt,
          projects,
          member_issue_uids: ["issue-1"],
          issues: [{ ...issue(), title: snapshotAttempts === 3 ? "Refreshed task" : issue().title }],
          enrichment: {
            selected_issue_uid: "issue-1",
            selected_detail: {
              detail: {
                ...detail(),
                issue: {
                  ...detail().issue,
                  title: snapshotAttempts === 3 ? "Refreshed task" : issue().title,
                },
              },
              etag: `"rev-${snapshotAttempts}"`,
              workspace_target: { available: false },
            },
            selected_history: [],
          },
        });
      }
      if (url.pathname.endsWith("/recurrences")) {
        return response({ recurrences: [], fetched_at: fetchedAt });
      }
      if (url.pathname.endsWith("/comments") && init?.method === "POST") {
        commentAttempts += 1;
        return response({ changed: true });
      }
      return response({ error: { code: "not_found", message: `Unhandled ${url.pathname}` } }, 404);
    });
    vi.stubGlobal("fetch", fetchImpl);

    render(KataWorkspaceSidebarPane, { props: { kata } });
    await screen.findByRole("heading", { name: issue().title });
    await fireEvent.input(screen.getByRole("textbox", { name: "Comment" }), {
      target: { value: "Submit once" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));

    await waitFor(() => expect(commentAttempts).toBe(1));
    await waitFor(() => expect(snapshotAttempts).toBe(2));
    expect(showFlash).not.toHaveBeenCalledWith("Snapshot refresh failed.", { tone: "danger" });
    expect((await screen.findByRole("alert")).textContent).toContain("saved");

    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));
    expect(commentAttempts).toBe(1);

    await fireEvent.click(screen.getByRole("button", { name: "Retry Kata snapshot" }));
    await waitFor(() => expect(snapshotAttempts).toBe(3));
    await screen.findByRole("heading", { name: "Refreshed task" });
    expect(commentAttempts).toBe(1);
  });
});
