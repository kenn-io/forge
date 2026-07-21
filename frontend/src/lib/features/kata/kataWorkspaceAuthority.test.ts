import { describe, expect, it } from "vite-plus/test";

import type { KataTaskEventStreamFrame } from "../../api/kata/eventStream.js";
import type { KataProjectSummary, KataTaskSearchFilters, KataTaskSummary } from "../../api/kata/taskTypes.js";
import {
  kataWorkspaceAuthorityRequest,
  projectKataWorkspaceView,
  shouldReloadKataWorkspaceForFrame,
} from "./kataWorkspaceAuthority.js";

const defaultFilters: KataTaskSearchFilters = {
  scope: { kind: "all" },
  status: "open",
  owner: "",
  label: "",
  query: "",
};

const projects: KataProjectSummary[] = [
  {
    id: 1,
    uid: "project-inbox",
    name: "Inbox",
    metadata: { role: "inbox" },
    open_count: 1,
  },
  {
    id: 2,
    uid: "project-work",
    name: "Work",
    metadata: {},
    open_count: 2,
  },
];

function issue(
  uid: string,
  title: string,
  projectUID: string,
  status: KataTaskSummary["status"] = "open",
  metadata: KataTaskSummary["metadata"] = {},
): KataTaskSummary {
  const project = projects.find((candidate) => candidate.uid === projectUID)!;
  return {
    id: uid.length,
    uid,
    project_id: project.id,
    short_id: uid,
    qualified_id: `${project.name}#${uid}`,
    title,
    status,
    project_uid: project.uid,
    project_name: project.name,
    metadata,
    revision: 1,
    author: "fixture-user",
    created_at: "2026-07-20T10:00:00Z",
    updated_at: "2026-07-20T11:00:00Z",
    ...(status === "closed" ? { closed_at: "2026-07-20T12:00:00Z" } : {}),
  };
}

describe("kata workspace authority request", () => {
  it.each([
    ["inbox", "open"],
    ["today", "open"],
    ["upcoming", "open"],
    ["deadlines", "open"],
    ["all", "open"],
    ["logbook", "closed"],
  ] as const)("maps the default %s system view to global %s authority", (view, authority) => {
    expect(kataWorkspaceAuthorityRequest({ daemonID: "home", view, filters: defaultFilters }).intent).toEqual({
      daemon_id: "home",
      scope: "global",
      authority,
    });
  });

  it.each([
    [{ scope: { kind: "project", project_uid: "project-work" }, status: "open" }, "project", "open"],
    [{ scope: { kind: "all" }, status: "ready" }, "global", "ready"],
    [{ scope: { kind: "all" }, status: "closed" }, "global", "closed"],
    [{ scope: { kind: "all" }, status: "all" }, "global", "all"],
  ] as const)("maps active filters to their exact snapshot scope and authority", (patch, scope, authority) => {
    const request = kataWorkspaceAuthorityRequest({
      daemonID: "home",
      view: "today",
      filters: { ...defaultFilters, ...patch },
    });

    expect(request.intent).toEqual({
      daemon_id: "home",
      scope,
      ...(scope === "project" ? { project_uid: "project-work" } : {}),
      authority,
    });
  });

  it("keeps owner, label, and query in local presentation instead of snapshot identity", () => {
    const request = kataWorkspaceAuthorityRequest({
      daemonID: "home",
      view: "all",
      filters: {
        ...defaultFilters,
        owner: " Alice ",
        label: " Urgent ",
        query: " Needle ",
      },
    });

    expect(request.intent).toEqual({ daemon_id: "home", scope: "global", authority: "open" });
    expect(request.presentation).toEqual({ owner: " Alice ", label: " Urgent ", text: " Needle " });
  });

  it("keeps Logbook on closed authority when only presentation filters change", () => {
    const request = kataWorkspaceAuthorityRequest({
      daemonID: "home",
      view: "logbook",
      filters: {
        ...defaultFilters,
        owner: "Alice",
        label: "Urgent",
        query: "Needle",
      },
    });

    expect(request.intent).toEqual({ daemon_id: "home", scope: "global", authority: "closed" });
    expect(request.presentation).toEqual({ owner: "Alice", label: "Urgent", text: "Needle" });
  });

  it("keeps selected detail and graph root independent in snapshot identity", () => {
    expect(
      kataWorkspaceAuthorityRequest({
        daemonID: "home",
        view: "all",
        filters: defaultFilters,
        selectedIssueUID: "issue-b",
        graphSourceUID: "issue-a",
      }).intent,
    ).toEqual({
      daemon_id: "home",
      scope: "global",
      authority: "open",
      selected_issue_uid: "issue-b",
      graph_source_uid: "issue-a",
    });
  });
});

describe("kata workspace view projection", () => {
  const snapshot = {
    projects,
    fetched_at: "2026-07-20T13:00:00Z",
  };

  it("reuses the system view builder for default views", () => {
    const view = projectKataWorkspaceView({
      view: "today",
      filters: defaultFilters,
      snapshot,
      issues: [
        issue("due", "Due today", "project-work", "open", { scheduled_on: "2026-07-20" }),
        issue("later", "Later", "project-work", "open", { scheduled_on: "2026-07-21" }),
      ],
      today: "2026-07-20",
    });

    expect(view).toEqual({
      view: "today",
      fetched_at: snapshot.fetched_at,
      groups: [{ id: "today", title: "Today", issues: [expect.objectContaining({ uid: "due" })] }],
    });
  });

  it("projects default Logbook from closed authority rows", () => {
    const view = projectKataWorkspaceView({
      view: "logbook",
      filters: defaultFilters,
      snapshot,
      issues: [issue("done", "Completed", "project-work", "closed")],
      today: "2026-07-20",
    });

    expect(view.groups).toEqual([
      { id: "2026-07-20", title: "2026-07-20", issues: [expect.objectContaining({ uid: "done" })] },
    ]);
  });

  it("projects active status-open Logbook filters as Results", () => {
    const openIssue = issue("open", "Open result", "project-work");
    const view = projectKataWorkspaceView({
      view: "logbook",
      filters: { ...defaultFilters, query: "Open" },
      snapshot,
      issues: [openIssue],
    });

    expect(view).toEqual({
      view: "logbook",
      fetched_at: snapshot.fetched_at,
      groups: [{ id: "search-results", title: "Results", issues: [openIssue] }],
    });
  });

  it("combines project scope with the selected system view", () => {
    const due = issue("due", "Project deadline", "project-work", "open", { deadline_on: "2026-07-20" });
    const backlog = issue("backlog", "Project backlog", "project-work");
    const view = projectKataWorkspaceView({
      view: "deadlines",
      filters: { ...defaultFilters, scope: { kind: "project", project_uid: "project-work" } },
      snapshot,
      issues: [due, backlog],
      today: "2026-07-20",
    });

    expect(view).toEqual({
      view: "deadlines",
      fetched_at: snapshot.fetched_at,
      groups: [{ id: "today", title: "Today", issues: [due] }],
    });
  });
});

describe("kata compact frame reload", () => {
  const snapshot = {
    server_instance_id: "server-a",
    daemon_id: "home",
    invalidation_epoch: 4,
    event_cursor: 52,
  };

  function frame(overrides: Partial<KataTaskEventStreamFrame> = {}): KataTaskEventStreamFrame {
    return {
      kind: "invalidation",
      server_instance_id: "server-a",
      daemon_id: "home",
      epoch: 5,
      cursor: 53,
      ...overrides,
    };
  }

  it("accepts a newer invalidation for the current daemon and server", () => {
    expect(shouldReloadKataWorkspaceForFrame(frame(), snapshot, "home")).toBe(true);
  });

  it.each([
    ["daemon mismatch", frame({ daemon_id: "work" })],
    ["server mismatch", frame({ server_instance_id: "server-b" })],
    ["stale epoch", frame({ epoch: 4 })],
    ["stale cursor", frame({ cursor: 52 })],
  ])("ignores %s invalidations", (_name, candidate) => {
    expect(shouldReloadKataWorkspaceForFrame(candidate, snapshot, "home")).toBe(false);
  });

  it("accepts a new-server reset even when its epoch and cursor move backwards", () => {
    expect(
      shouldReloadKataWorkspaceForFrame(
        frame({ kind: "reset", server_instance_id: "server-b", epoch: 0, cursor: 1 }),
        snapshot,
        "home",
      ),
    ).toBe(true);
  });

  it("ignores a reset already covered by the accepted snapshot", () => {
    expect(shouldReloadKataWorkspaceForFrame(frame({ kind: "reset", epoch: 4, cursor: 52 }), snapshot, "home")).toBe(
      false,
    );
  });

  it("only accepts reset frames before the first snapshot", () => {
    expect(shouldReloadKataWorkspaceForFrame(frame({ kind: "reset" }), null, "home")).toBe(true);
    expect(shouldReloadKataWorkspaceForFrame(frame(), null, "home")).toBe(false);
  });
});
