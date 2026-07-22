import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";

import KataWorkspace from "./KataWorkspace.svelte";
import { KATA_WORKSPACE_STATE_STORAGE_KEY } from "./kataWorkspacePersistence.js";
import {
  createWorkspaceAPI,
  deferred,
  detail,
  fetchedAt,
  initialIssues,
  resetKataWorkspaceTestState,
} from "./test/KataWorkspaceSupport.js";

const { mockCreateKataWorkspaceForTask, mockNavigate } = vi.hoisted(() => ({
  mockCreateKataWorkspaceForTask: vi.fn(),
  mockNavigate: vi.fn(),
}));

vi.mock("../../api/kata/workspaces.js", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/kata/workspaces.js")>();
  return {
    ...actual,
    createKataWorkspaceForTask: mockCreateKataWorkspaceForTask,
  };
});

vi.mock("../../stores/router.svelte.js", () => ({ navigate: mockNavigate }));

vi.mock("./KataReachableGraph.svelte", async () => ({
  default: (await import("./KataReachableGraphTestStub.svelte")).default,
}));

function acceptHomeDaemon(): void {
  vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
    Response.json({
      daemons: [
        {
          id: "home",
          url: "http://127.0.0.1:7777",
          default: true,
          auth: "none",
          health: "connected",
        },
      ],
    }),
  );
}

async function waitForWorkspaceWritable(): Promise<void> {
  await waitFor(() =>
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false),
  );
}

describe("KataWorkspace snapshot authority", () => {
  beforeEach(() => {
    resetKataWorkspaceTestState();
    mockCreateKataWorkspaceForTask.mockReset();
    mockNavigate.mockReset();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders selected detail, bounded history, workspace target, and mutation ETag from one accepted snapshot", async () => {
    acceptHomeDaemon();
    const snapshotETag = '"snapshot-etag"';
    const selected = initialIssues[0]!;
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: (_request, snapshot) => ({
        ...snapshot,
        enrichment: {
          ...snapshot.enrichment,
          selected_detail: {
            detail: {
              ...detail(selected.uid, initialIssues),
              issue: { ...detail(selected.uid, initialIssues).issue, title: "Snapshot-selected task" },
            },
            etag: snapshotETag,
            workspace_target: {
              available: true,
              existing_workspace: { id: "workspace-snapshot", status: "ready" },
              item_type: "kata_task",
              item_key: selected.uid,
            },
          },
          selected_history: [
            {
              event_id: 17,
              event_uid: "event-17",
              origin_instance_uid: "instance-1",
              content_hash: "hash-17",
              hlc_counter: 0,
              hlc_physical_ms: Date.parse(fetchedAt),
              type: "issue.commented",
              project_id: selected.project_id,
              project_uid: selected.project_uid,
              project_name: selected.project_name,
              issue_id: selected.id,
              issue_uid: selected.uid,
              issue_short_id: selected.short_id,
              actor: "snapshot-user",
              created_at: fetchedAt,
            },
          ],
        },
      }),
    });
    const patchIssueMetadata = vi.fn(async () => ({
      changed: true,
      issue: { ...selected, title: "Mutation response title" },
      etag: '"response-etag"',
    }));
    api.patchIssueMetadata = patchIssueMetadata;

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("heading", { name: "Snapshot-selected task" });
    expect(screen.getByText("commented")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open workspace" })).toBeTruthy();

    await waitForWorkspaceWritable();
    await fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    await fireEvent.click(
      within(screen.getByRole("menu", { name: "Task actions" })).getByRole("menuitem", { name: "Add checklist" }),
    );
    await fireEvent.input(screen.getByRole("textbox", { name: "New checklist item" }), {
      target: { value: "Use accepted snapshot" },
    });
    await fireEvent.keyDown(screen.getByRole("textbox", { name: "New checklist item" }), { key: "Enter" });

    await waitFor(() =>
      expect(patchIssueMetadata).toHaveBeenCalledWith(
        { project_id: selected.project_id, ref: selected.uid },
        "middleman",
        expect.objectContaining({ checklist: expect.any(Array) }),
        snapshotETag,
        { daemonId: "home" },
      ),
    );
    expect(screen.queryByRole("heading", { name: "Mutation response title" })).toBeNull();
  });

  it("renders canonical catalog identity when selected detail protocol omits summary-only fields", async () => {
    acceptHomeDaemon();
    const selected = initialIssues[0]!;
    const selectedDetail = detail(selected.uid, initialIssues);
    const {
      project_uid: _projectUID,
      project_name: _projectName,
      qualified_id: _qualifiedID,
      ...protocolIssue
    } = selectedDetail.issue;
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: (_request, snapshot) => ({
        ...snapshot,
        enrichment: {
          ...snapshot.enrichment,
          selected_detail: {
            workspace_target: { available: false },
            detail: { ...selectedDetail, issue: protocolIssue },
          },
        },
      }),
    });

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("heading", { name: selected.title });
    await fireEvent.click(screen.getByRole("button", { name: "Complete" }));

    expect(within(screen.getByRole("dialog", { name: "Complete task" })).getByText(selected.qualified_id)).toBeTruthy();
  });

  it("reveals a routed selected member child through its accepted catalog ancestor chain", async () => {
    acceptHomeDaemon();
    const parent = {
      ...initialIssues[0]!,
      uid: "issue-parent",
      short_id: "parent",
      qualified_id: "Finances#parent",
      title: "Parent task",
      child_counts: { open: 1, total: 1 },
    };
    const child = {
      ...initialIssues[0]!,
      uid: "issue-child",
      short_id: "child",
      qualified_id: "Finances#child",
      title: "Child task",
      parent: { uid: parent.uid, short_id: parent.short_id },
      parent_short_id: parent.short_id,
      child_counts: undefined,
    };
    const { api } = createWorkspaceAPI([parent, child]);

    render(KataWorkspace, { props: { api, selectedIssueUID: child.uid } });

    await screen.findByRole("button", { name: /Parent task/ });
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Parent task/ }).getAttribute("aria-expanded")).toBe("true"),
    );
    const childRow = screen.getByRole("button", { name: /Child task/ });
    expect(childRow.getAttribute("aria-current")).toBe("true");
  });

  it("reveals a persisted selected member child from the accepted snapshot", async () => {
    acceptHomeDaemon();
    const parent = {
      ...initialIssues[0]!,
      uid: "issue-persisted-parent",
      short_id: "persisted-parent",
      qualified_id: "Finances#persisted-parent",
      title: "Persisted parent task",
      child_counts: { open: 1, total: 1 },
    };
    const child = {
      ...initialIssues[0]!,
      uid: "issue-persisted-child",
      short_id: "persisted-child",
      qualified_id: "Finances#persisted-child",
      title: "Persisted child task",
      parent: { uid: parent.uid, short_id: parent.short_id },
      parent_short_id: parent.short_id,
      child_counts: undefined,
    };
    localStorage.setItem(
      KATA_WORKSPACE_STATE_STORAGE_KEY,
      JSON.stringify({
        version: 2,
        daemons: {
          home: {
            view: "all",
            filters: { scope: { kind: "all" }, status: "open", owner: "", label: "", query: "" },
            selectedIssueUID: child.uid,
          },
        },
      }),
    );
    const { api } = createWorkspaceAPI([parent, child]);

    render(KataWorkspace, { props: { api } });

    await screen.findByRole("button", { name: /Persisted parent task/ });
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /Persisted parent task/ }).getAttribute("aria-expanded")).toBe("true"),
    );
    expect(screen.getByRole("button", { name: /Persisted child task/ }).getAttribute("aria-current")).toBe("true");
  });

  it("preserves focused child selection without replacing the visible hierarchy branch", async () => {
    acceptHomeDaemon();
    const parent = {
      ...initialIssues[0]!,
      uid: "issue-focus-parent",
      short_id: "focus-parent",
      qualified_id: "Finances#focus-parent",
      title: "Focus parent task",
      child_counts: { open: 1, total: 1 },
    };
    const child = {
      ...initialIssues[0]!,
      uid: "issue-focus-child",
      short_id: "focus-child",
      qualified_id: "Finances#focus-child",
      title: "Focus child task",
      parent: { uid: parent.uid, short_id: parent.short_id },
      parent_short_id: parent.short_id,
      child_counts: undefined,
    };
    const { api } = createWorkspaceAPI([parent, child]);

    render(KataWorkspace, { props: { api } });

    const parentRow = await screen.findByRole("button", { name: /Focus parent task/ });
    parentRow.focus();
    await fireEvent.keyDown(parentRow, { key: "ArrowRight" });
    const childRow = await screen.findByRole("button", { name: /Focus child task/ });

    childRow.focus();
    await fireEvent.click(childRow);
    await screen.findByRole("heading", { name: child.title });
    await waitFor(() => expect((document.activeElement as HTMLElement | null)?.dataset.uid).toBe(child.uid));
  });

  it("re-reveals a focused selected child when an accepted snapshot resets its expanded hierarchy", async () => {
    acceptHomeDaemon();
    const parent = {
      ...initialIssues[0]!,
      uid: "issue-reset-focused-parent",
      short_id: "reset-focused-parent",
      qualified_id: "Finances#reset-focused-parent",
      title: "Reset focused parent",
      child_counts: { open: 1, total: 1 },
    };
    const child = {
      ...initialIssues[0]!,
      uid: "issue-reset-focused-child",
      short_id: "reset-focused-child",
      qualified_id: "Finances#reset-focused-child",
      title: "Reset focused child",
      parent: { uid: parent.uid, short_id: parent.short_id },
      parent_short_id: parent.short_id,
      child_counts: undefined,
    };
    let selectedSnapshots = 0;
    const { api } = createWorkspaceAPI([parent, child], {
      snapshot: (request, snapshot) => {
        if (request.selectedIssueUID !== child.uid || ++selectedSnapshots < 2) return snapshot;
        if (!snapshot.issues) throw new Error("expected selected snapshot issues");
        return {
          ...snapshot,
          issues: snapshot.issues.map((issue) =>
            issue.uid === parent.uid ? { ...issue, revision: issue.revision + 1 } : issue,
          ),
        };
      },
    });
    const onSelectedIssueChange = vi.fn();
    const scrollIntoView = vi.fn();
    vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(scrollIntoView);

    render(KataWorkspace, { props: { api, onSelectedIssueChange } });

    const parentRow = await screen.findByRole("button", { name: /Reset focused parent/ });
    await fireEvent.keyDown(parentRow, { key: "ArrowRight" });
    let childRow = await screen.findByRole("button", { name: /Reset focused child/ });
    childRow.focus();
    await fireEvent.click(childRow);
    await screen.findByRole("heading", { name: child.title });
    await waitFor(() => expect(onSelectedIssueChange).toHaveBeenCalledTimes(1));

    childRow = screen.getByRole("button", { name: /Reset focused child/ });
    childRow.focus();
    scrollIntoView.mockClear();
    await fireEvent.click(childRow);
    await waitFor(() => expect(onSelectedIssueChange).toHaveBeenCalledTimes(2));
    await tick();

    const refreshedChildRow = screen.getByRole("button", { name: /Reset focused child/ });
    expect(refreshedChildRow.getAttribute("aria-current")).toBe("true");
    expect(screen.getByRole("button", { name: /Reset focused parent/ }).getAttribute("aria-expanded")).toBe("true");
    expect((document.activeElement as HTMLElement | null)?.dataset.uid).toBe(child.uid);
    expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" });
  });

  it("keeps the existing scrolled task list when accepting a visible row selection", async () => {
    acceptHomeDaemon();
    const selected = initialIssues[1]!;
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: (request, snapshot) => ({
        ...snapshot,
        fetched_at: request.selectedIssueUID ? "2026-05-15T16:00:01.000Z" : snapshot.fetched_at,
      }),
    });
    const onSelectedIssueChange = vi.fn();

    const { container } = render(KataWorkspace, { props: { api, onSelectedIssueChange } });

    const selectedRow = await screen.findByRole("button", { name: new RegExp(selected.title) });
    const tableBody = container.querySelector(".table-body") as HTMLDivElement;
    tableBody.scrollTop = 48;

    await fireEvent.click(selectedRow);
    await screen.findByRole("heading", { name: selected.title });
    await waitFor(() => expect(onSelectedIssueChange).toHaveBeenCalledWith(selected.uid));
    await tick();

    expect(container.querySelector(".table-body")).toBe(tableBody);
    expect(tableBody.scrollTop).toBe(48);
  });

  it("does not install mutation response task state while explicitly revalidating snapshot authority", async () => {
    acceptHomeDaemon();
    const selected = initialIssues[0]!;
    let snapshotRequests = 0;
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: (_request, snapshot) => {
        snapshotRequests += 1;
        return {
          ...snapshot,
          enrichment: {
            ...snapshot.enrichment,
            selected_detail: {
              detail: {
                ...detail(selected.uid, initialIssues),
                issue: {
                  ...detail(selected.uid, initialIssues).issue,
                  title: "Accepted snapshot title",
                },
              },
              etag: '"snapshot-etag"',
              workspace_target: { available: false },
            },
          },
        };
      },
    });
    const addComment = vi.fn(async () => ({
      changed: true,
      issue: { ...selected, title: "Unaccepted mutation response title", revision: selected.revision + 1 },
      etag: '"mutation-response-etag"',
    }));
    api.addComment = addComment;

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("heading", { name: "Accepted snapshot title" });
    await waitForWorkspaceWritable();
    await fireEvent.input(screen.getByRole("textbox", { name: "Comment" }), {
      target: { value: "Wait for compact invalidation" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Add comment" }));

    await waitFor(() => expect(addComment).toHaveBeenCalledOnce());
    await Promise.resolve();

    expect(snapshotRequests).toBe(2);
    expect(screen.getByRole("heading", { name: "Accepted snapshot title" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Unaccepted mutation response title" })).toBeNull();
  });

  it("fences mutation controls without closing an owner draft when assignment transport fails", async () => {
    acceptHomeDaemon();
    const selected = initialIssues[0]!;
    const { api } = createWorkspaceAPI(initialIssues);
    const assignment = deferred<{ changed: boolean }>();
    const assignOwner = vi.fn(() => assignment.promise);
    api.assignOwner = assignOwner;

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("heading", { name: selected.title });
    await waitForWorkspaceWritable();
    await fireEvent.click(screen.getByRole("button", { name: "Owner: fixture-user" }));
    const ownerInput = screen.getByRole("combobox", { name: "Owner" }) as HTMLInputElement;
    await fireEvent.input(ownerInput, { target: { value: "agent:new" } });
    await fireEvent.keyDown(ownerInput, { key: "Enter" });

    await waitFor(() => expect(assignOwner).toHaveBeenCalledOnce());
    expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(true);
    expect((document.querySelector(".kata-detail") as HTMLElement & { inert: boolean }).inert).toBe(false);
    expect(screen.getByRole("button", { name: "More actions" }).matches(":disabled")).toBe(true);
    expect(ownerInput.disabled).toBe(false);
    await fireEvent.keyDown(ownerInput, { key: "Enter" });
    expect(assignOwner).toHaveBeenCalledOnce();
    assignment.reject(new Error("owner unavailable"));

    await waitFor(() =>
      expect((screen.getByRole("button", { name: "New task" }) as HTMLButtonElement).disabled).toBe(false),
    );
    expect((screen.getByRole("combobox", { name: "Owner" }) as HTMLInputElement).value).toBe("agent:new");
    expect((screen.getByRole("region", { name: "Task detail" }) as HTMLElement & { inert: boolean }).inert).toBe(false);
  });

  it("keeps the graph source fixed while selecting another snapshot graph node", async () => {
    acceptHomeDaemon();
    const requests: Array<{ selectedIssueUID?: string; graphSourceUID?: string }> = [];
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: (request, snapshot) => {
        requests.push({
          ...(request.selectedIssueUID ? { selectedIssueUID: request.selectedIssueUID } : {}),
          ...(request.graphSourceUID ? { graphSourceUID: request.graphSourceUID } : {}),
        });
        return snapshot;
      },
    });
    const onSelectedIssueChange = vi.fn();

    render(KataWorkspace, {
      props: {
        api,
        selectedIssueUID: initialIssues[0]!.uid,
        onSelectedIssueChange,
      },
    });

    await screen.findByRole("heading", { name: initialIssues[0]!.title });
    const sourceRow = screen.getByRole("button", { name: new RegExp(initialIssues[0]!.title) });
    await fireEvent.click(within(sourceRow.parentElement!).getByRole("button", { name: "Open reachable graph" }));
    await waitFor(() =>
      expect(requests.at(-1)).toEqual({
        selectedIssueUID: initialIssues[0]!.uid,
        graphSourceUID: initialIssues[0]!.uid,
      }),
    );

    await fireEvent.click(await screen.findByRole("button", { name: new RegExp(initialIssues[1]!.title) }));
    await waitFor(() =>
      expect(requests.at(-1)).toEqual({
        selectedIssueUID: initialIssues[1]!.uid,
        graphSourceUID: initialIssues[0]!.uid,
      }),
    );
    await waitFor(() => expect(onSelectedIssueChange).toHaveBeenCalledWith(initialIssues[1]!.uid));
    expect(screen.getByRole("button", { name: "Back to task list" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Back to task list" }));
    await waitFor(() => expect(requests.at(-1)?.graphSourceUID).toBeUndefined());
  });

  it("shows a graph enrichment error and retries the exact graph intent", async () => {
    acceptHomeDaemon();
    const selected = initialIssues[0]!;
    const requests: Array<{ selectedIssueUID?: string; graphSourceUID?: string }> = [];
    let graphAttempts = 0;
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: (request, snapshot) => {
        requests.push({
          ...(request.selectedIssueUID ? { selectedIssueUID: request.selectedIssueUID } : {}),
          ...(request.graphSourceUID ? { graphSourceUID: request.graphSourceUID } : {}),
        });
        if (!request.graphSourceUID || ++graphAttempts > 1) return snapshot;
        const { graph: _graph, graph_fetched_at: _graphFetchedAt, ...enrichment } = snapshot.enrichment;
        return {
          ...snapshot,
          enrichment: {
            ...enrichment,
            errors: { graph: { code: "graph_failed", message: "Graph load failed." } },
          },
        };
      },
    });

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("heading", { name: selected.title });
    const sourceRow = screen.getByRole("button", { name: new RegExp(selected.title) });
    await fireEvent.click(within(sourceRow.parentElement!).getByRole("button", { name: "Open reachable graph" }));

    expect((await screen.findByRole("alert")).textContent).toContain("Graph load failed.");
    expect(screen.queryByText("Loading graph...")).toBeNull();
    expect(screen.getByRole("button", { name: "Back to task list" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Retry graph" }));

    await waitFor(() =>
      expect(requests.at(-1)).toEqual({
        selectedIssueUID: selected.uid,
        graphSourceUID: selected.uid,
      }),
    );
    expect(await screen.findByRole("button", { name: new RegExp(initialIssues[1]!.title) })).toBeTruthy();
  });

  it("shows selected detail enrichment failure instead of an empty selection", async () => {
    acceptHomeDaemon();
    const selected = initialIssues[0]!;
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: (request, snapshot) => {
        if (!request.selectedIssueUID) return snapshot;
        const { selected_detail: _detail, ...enrichment } = snapshot.enrichment;
        return {
          ...snapshot,
          enrichment: {
            ...enrichment,
            errors: { detail: { code: "detail_failed", message: "Detail load failed." } },
          },
        };
      },
    });

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    expect((await screen.findByRole("alert")).textContent).toContain("Detail load failed.");
    expect(screen.queryByText("Select a task")).toBeNull();
  });

  it("shows selected history enrichment failure while retaining accepted detail", async () => {
    acceptHomeDaemon();
    const selected = initialIssues[0]!;
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: (request, snapshot) =>
        request.selectedIssueUID
          ? {
              ...snapshot,
              enrichment: {
                ...snapshot.enrichment,
                selected_history: [],
                errors: { history: { code: "history_failed", message: "History load failed." } },
              },
            }
          : snapshot,
    });

    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    expect(await screen.findByRole("heading", { name: selected.title })).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("History load failed.");
  });

  it("renders linked peer titles from the bounded snapshot catalog", async () => {
    acceptHomeDaemon();
    const selected = initialIssues[0]!;
    const linked = initialIssues[1]!;
    const linkedDetail = detail(selected.uid, initialIssues);
    linkedDetail.links = [
      {
        id: 1,
        project_id: selected.project_id,
        from: { uid: selected.uid, short_id: selected.short_id },
        to: { uid: linked.uid, short_id: linked.short_id },
        type: "related",
        author: "middleman",
        created_at: fetchedAt,
      },
    ];
    const { api } = createWorkspaceAPI(initialIssues, {
      snapshot: (_request, snapshot) => ({
        ...snapshot,
        enrichment: {
          ...snapshot.enrichment,
          selected_detail: {
            detail: linkedDetail,
            etag: '"snapshot-etag"',
            workspace_target: { available: false },
          },
        },
      }),
    });
    render(KataWorkspace, { props: { api, selectedIssueUID: selected.uid } });

    await screen.findByRole("heading", { name: selected.title });
    expect(
      within(screen.getByRole("region", { name: "Links" })).getByRole("button", { name: new RegExp(linked.title) }),
    ).toBeTruthy();
  });

  it("keeps relationship filters across task navigation and resets state filters with the workspace scope", async () => {
    const root = issue("issue-root", "Root task", "project-kata");
    const next = issue("issue-next", "Next task", "project-kata");
    const related = issue("issue-related", "Related task", "project-kata");
    const blocked = issue("issue-blocked", "Blocked task", "project-kata");
    const closed = {
      ...issue("issue-closed", "Closed task", "project-kata"),
      status: "closed" as const,
      closed_reason: "done" as const,
      closed_at: fetchedAt,
    };
    const rows = [root, next, related, blocked, closed];
    const links: KataTaskLink[] = [
      {
        id: 1,
        project_id: root.project_id,
        from: { uid: root.uid, short_id: root.short_id },
        to: { uid: related.uid, short_id: related.short_id },
        type: "related",
        author: "fixture-user",
        created_at: fetchedAt,
      },
      {
        id: 2,
        project_id: root.project_id,
        from: { uid: root.uid, short_id: root.short_id },
        to: { uid: blocked.uid, short_id: blocked.short_id },
        type: "blocks",
        author: "fixture-user",
        created_at: fetchedAt,
      },
    ];
    const { api } = createWorkspaceAPI(rows);
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      const taskDetail = detail(uid, rows);
      return uid === root.uid ? { ...taskDetail, links } : taskDetail;
    });

    render(KataWorkspaceRouteHost, { props: { api, initialIssue: root.uid } });

    await waitFor(() => expect(screen.getByRole("heading", { name: "Root task" })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Filter links" }));
    await fireEvent.click(screen.getByRole("checkbox", { name: "Related" }));

    const issues = screen.getByRole("main", { name: "Issues" });
    await fireEvent.click(within(issues).getByRole("button", { name: /Next task/ }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Next task" })).toBeTruthy());
    await fireEvent.click(within(issues).getByRole("button", { name: /Root task/ }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Root task" })).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Filter links" }));
    expect((screen.getByRole("checkbox", { name: "Related" }) as HTMLInputElement).checked).toBe(false);
    await fireEvent.keyDown(document, { key: "Escape" });

    await fireEvent.click(screen.getByRole("combobox", { name: "Status: Open" }));
    await fireEvent.click(screen.getByRole("option", { name: "All" }));
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Status: All" })).toBeTruthy());
    expect(screen.getByRole("heading", { name: "Root task" })).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Filter links" }));
    expect((screen.getByRole("checkbox", { name: "Open" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("checkbox", { name: "Closed" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("checkbox", { name: "Related" }) as HTMLInputElement).checked).toBe(false);
  });

  it("resets the complete link-filter scope when switching daemons", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json({
        daemons: [
          { id: "home", url: "http://127.0.0.1:7777", default: true, auth: "none", health: "connected" },
          { id: "work", url: "http://127.0.0.1:8888", default: false, auth: "none", health: "connected" },
        ],
      }),
    );
    setActiveKataDaemon("home");
    const homeRoot = issue("issue-home-root", "Home root", "project-kata");
    const homePeer = issue("issue-home-peer", "Home peer", "project-kata");
    const workRoot = issue("issue-work-root", "Work root", "project-kata");
    const api = createDaemonWorkspaceAPI({ home: [homeRoot, homePeer], work: [workRoot] });
    vi.mocked(api.issue).mockImplementation(async (uid: string) => {
      const rows = [homeRoot, homePeer, workRoot];
      const taskDetail = detail(uid, rows);
      if (uid !== homeRoot.uid) return taskDetail;
      return {
        ...taskDetail,
        links: [
          {
            id: 1,
            project_id: homeRoot.project_id,
            from: { uid: homeRoot.uid, short_id: homeRoot.short_id },
            to: { uid: homePeer.uid, short_id: homePeer.short_id },
            type: "related",
            author: "fixture-user",
            created_at: fetchedAt,
          },
        ],
      };
    });

    render(KataWorkspaceRouteHost, { props: { api, initialIssue: homeRoot.uid } });
    await waitFor(() => expect(screen.getByRole("heading", { name: "Home root" })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Filter links" }));
    await fireEvent.click(screen.getByRole("checkbox", { name: "Related" }));
    await fireEvent.keyDown(document, { key: "Escape" });

    await fireEvent.click(screen.getByTestId("daemon-chip"));
    const workDaemonRow = await screen.findByTestId("daemon-row-work");
    await waitFor(() => expect((workDaemonRow as HTMLButtonElement).disabled).toBe(false));
    await fireEvent.click(workDaemonRow);
    await waitFor(() => expect(screen.getByRole("heading", { name: "Work root" })).toBeTruthy());

    await fireEvent.click(screen.getByRole("button", { name: "Filter links" }));
    expect((screen.getByRole("checkbox", { name: "Open" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("checkbox", { name: "Closed" }) as HTMLInputElement).checked).toBe(false);
    expect((screen.getByRole("checkbox", { name: "Related" }) as HTMLInputElement).checked).toBe(true);
  });
});
