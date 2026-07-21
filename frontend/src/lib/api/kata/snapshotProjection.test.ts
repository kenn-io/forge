import { describe, expect, it } from "vite-plus/test";

import type { KataWorkspaceSnapshotResponse } from "./snapshot.js";
import { normalizeKataWorkspaceSnapshot } from "./snapshotProjection.js";

type SnapshotIssue = NonNullable<KataWorkspaceSnapshotResponse["issues"]>[number];
type SnapshotProject = NonNullable<KataWorkspaceSnapshotResponse["projects"]>[number];

function project(overrides: Partial<SnapshotProject> = {}): SnapshotProject {
  return {
    id: 7,
    uid: "project-a",
    name: "Project A",
    metadata: { area: "work" },
    revision: 3,
    created_at: "2026-07-20T10:00:00Z",
    open_count: 2,
    closed_count: 1,
    last_event_at: "2026-07-20T11:00:00Z",
    ...overrides,
  };
}

function issue(overrides: Partial<SnapshotIssue> = {}): SnapshotIssue {
  return {
    id: 11,
    uid: "issue-authority",
    project_id: 7,
    project_uid: "project-a",
    project_name: "Project A",
    short_id: "a1",
    qualified_id: "Project A#a1",
    title: "Authority task",
    body: "Task body",
    status: "open",
    metadata: { scheduled_on: "2026-07-21" },
    revision: 5,
    author: "alice",
    labels: ["urgent"],
    blocks: null,
    blocked_by: null,
    related: null,
    created_at: "2026-07-20T10:00:00Z",
    updated_at: "2026-07-20T11:00:00Z",
    ...overrides,
  };
}

function snapshot(overrides: Partial<KataWorkspaceSnapshotResponse> = {}): KataWorkspaceSnapshotResponse {
  return {
    server_instance_id: "server-a",
    daemon_id: "home",
    intent: { scope: "global", authority: "all" },
    generation: 9,
    invalidation_epoch: 4,
    event_cursor: 31,
    fetched_at: "2026-07-20T12:00:00Z",
    projects: [project()],
    member_issue_uids: ["issue-authority"],
    issues: [issue()],
    enrichment: {},
    ...overrides,
  };
}

describe("normalizeKataWorkspaceSnapshot", () => {
  it("normalizes nullable arrays to empty immutable projections", () => {
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({
        projects: null,
        member_issue_uids: null,
        issues: null,
        enrichment: {
          selected_history: null,
          graph: {
            source_uid: "issue-root",
            depth: "full",
            hide_done: false,
            nodes: null,
            edges: null,
            unresolved_refs: null,
          },
        },
      }),
    );

    expect({
      projects: projection.projects,
      memberIssueUIDs: projection.member_issue_uids,
      issues: projection.issues,
      selectedHistory: projection.selected_history,
      graphNodes: projection.graph?.nodes,
      graphEdges: projection.graph?.edges,
      graphUnresolved: projection.graph?.unresolved_refs,
      memberSetSize: projection.member_issue_uid_set.size,
    }).toEqual({
      projects: [],
      memberIssueUIDs: [],
      issues: [],
      selectedHistory: [],
      graphNodes: [],
      graphEdges: [],
      graphUnresolved: [],
      memberSetSize: 0,
    });
    expect(Object.isFrozen(projection)).toBe(true);
    expect(Object.isFrozen(projection.projects)).toBe(true);
    expect(() => (projection.projects as SnapshotProject[]).push(project())).toThrow();
    expect(() => (projection.member_issue_uid_set as Set<string>).add("issue-new")).toThrow();
  });

  it("normalizes complete projects, authority issues, and membership", () => {
    const projection = normalizeKataWorkspaceSnapshot(snapshot());

    expect(projection.projects).toEqual([
      {
        id: 7,
        uid: "project-a",
        name: "Project A",
        metadata: { area: "work" },
        revision: 3,
        created_at: "2026-07-20T10:00:00Z",
        open_count: 2,
      },
    ]);
    expect(projection.issues).toEqual([
      expect.objectContaining({
        uid: "issue-authority",
        status: "open",
        labels: ["urgent"],
        blocks: [],
        blocked_by: [],
        related: [],
      }),
    ]);
    expect(projection.member_issue_uids).toEqual(["issue-authority"]);
    expect(projection.member_issue_uid_set.has("issue-authority")).toBe(true);
  });

  it("narrows and freezes a valid project authority intent", () => {
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({ intent: { scope: "project", project_uid: "project-a", authority: "ready" } }),
    );

    expect(projection.intent).toEqual({ scope: "project", project_uid: "project-a", authority: "ready" });
    expect(Object.isFrozen(projection.intent)).toBe(true);
  });

  it("never exposes the mutable membership backing set to forEach callbacks", () => {
    const projection = normalizeKataWorkspaceSnapshot(snapshot());
    let callbackSet: ReadonlySet<string> | undefined;

    projection.member_issue_uid_set.forEach((_value, _key, set) => {
      callbackSet = set;
    });

    expect(callbackSet).toBe(projection.member_issue_uid_set);
    expect(() => (callbackSet as Set<string>).clear()).toThrow();
    expect(projection.member_issue_uid_set.has("issue-authority")).toBe(true);
  });

  it("does not expose the mutable membership backing set through valueOf", () => {
    const projection = normalizeKataWorkspaceSnapshot(snapshot());
    const value = projection.member_issue_uid_set.valueOf();

    expect(value).toBe(projection.member_issue_uid_set);
    expect(() => (value as Set<string>).clear()).toThrow();
    expect(projection.member_issue_uid_set.has("issue-authority")).toBe(true);
  });

  it("combines selected detail with its generated ETag and workspace target", () => {
    const workspaceTarget = { available: false };
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({
        enrichment: {
          selected_issue_uid: "issue-authority",
          selected_detail: {
            etag: '"revision-5"',
            workspace_target: workspaceTarget,
            detail: {
              issue: issue({ body: "Full selected body" }),
              comments: [],
              labels: [],
              links: [],
              children: [],
            },
          },
        },
      }),
    );

    expect(projection.selected_issue_uid).toBe("issue-authority");
    expect(projection.selected_detail).toMatchObject({
      issue: { uid: "issue-authority", body: "Full selected body" },
      etag: '"revision-5"',
      workspace_target: workspaceTarget,
    });
    expect(Object.isFrozen(projection.selected_detail?.workspace_target)).toBe(true);
  });

  it("merges canonical selected identity from the accepted catalog row", () => {
    const catalogIssue = issue({
      project_uid: "project-canonical",
      project_name: "Canonical Project",
      qualified_id: "Canonical Project#a1",
    });
    const {
      project_uid: _projectUID,
      project_name: _projectName,
      qualified_id: _qualifiedID,
      ...protocolIssue
    } = issue({ body: "Full selected body" });
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({
        member_issue_uids: [catalogIssue.uid],
        issues: [catalogIssue],
        enrichment: {
          selected_issue_uid: catalogIssue.uid,
          selected_detail: {
            workspace_target: { available: false },
            detail: {
              issue: protocolIssue,
              comments: [],
              labels: [],
              links: [],
              children: [],
            },
          },
        },
      }),
    );

    expect(projection.selected_detail?.issue).toMatchObject({
      uid: catalogIssue.uid,
      body: "Full selected body",
      project_uid: "project-canonical",
      project_name: "Canonical Project",
      qualified_id: "Canonical Project#a1",
    });
  });

  it("drops selected detail whose issue UID differs from the declared selected UID", () => {
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({
        enrichment: {
          selected_issue_uid: "issue-authority",
          selected_detail: {
            workspace_target: { available: false },
            detail: {
              issue: issue({ uid: "issue-other" }),
              comments: [],
              labels: [],
              links: [],
              children: [],
            },
          },
        },
      }),
    );

    expect(projection.selected_issue_uid).toBe("issue-authority");
    expect(projection.selected_detail).toBeUndefined();
    expect(projection.enrichment_errors.detail).toEqual({
      code: "invalid_snapshot_enrichment",
      message: "Could not normalize selected task detail.",
    });
  });

  it("normalizes bounded selected history envelopes without legacy cursor fields", () => {
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({
        enrichment: {
          selected_history: [
            {
              event_id: 19,
              event_uid: "event-19",
              origin_instance_uid: "daemon-instance",
              type: "issue.updated",
              project_id: 7,
              project_uid: "project-a",
              project_name: "Project A",
              issue_id: 11,
              issue_uid: "issue-authority",
              issue_short_id: "a1",
              actor: "alice",
              payload: { title: "Updated task" },
              created_at: "2026-07-20T11:30:00Z",
              content_hash: "sha256:abc",
              hlc_counter: 2,
              hlc_physical_ms: 1_753_012_200_000,
            },
          ],
        },
      }),
    );

    expect(projection.selected_history).toEqual([
      {
        event_id: 19,
        event_uid: "event-19",
        origin_instance_uid: "daemon-instance",
        type: "issue.updated",
        project_id: 7,
        project_uid: "project-a",
        project_name: "Project A",
        issue_id: 11,
        issue_uid: "issue-authority",
        issue_short_id: "a1",
        related_issue_id: undefined,
        related_issue_uid: undefined,
        related_issue_short_id: undefined,
        actor: "alice",
        payload: { title: "Updated task" },
        created_at: "2026-07-20T11:30:00Z",
      },
    ]);
    expect(projection).not.toHaveProperty("next_after_id");
    expect(projection).not.toHaveProperty("reset_required");
  });

  it("keeps graph enrichment separate from authority membership and selection", () => {
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({
        graph_source_uid: "issue-root",
        enrichment: {
          selected_issue_uid: "issue-selected",
          graph_fetched_at: "2026-07-20T11:45:00Z",
          graph: {
            source_uid: "issue-root",
            depth: "2",
            hide_done: true,
            nodes: [
              {
                id: 12,
                uid: "issue-graph-only",
                project_id: 7,
                project_uid: "project-a",
                short_id: "a2",
                qualified_id: "a2",
                title: "Graph-only task",
                body: "Graph body",
                status: "closed",
                metadata: {},
                revision: 6,
                author: "bob",
                created_at: "2026-07-20T10:10:00Z",
                updated_at: "2026-07-20T11:10:00Z",
              },
            ],
            edges: [{ from_uid: "issue-root", to_uid: "issue-graph-only", kind: "blocks", layout: true }],
            unresolved_refs: [],
          },
        },
      }),
    );

    expect(projection.selected_issue_uid).toBe("issue-selected");
    expect(projection.graph_source_uid).toBe("issue-root");
    expect(projection.graph_fetched_at).toBe("2026-07-20T11:45:00Z");
    expect(projection.graph).toMatchObject({
      source_uid: "issue-root",
      depth: "2",
      hide_done: true,
      fetched_at: "2026-07-20T11:45:00Z",
      nodes: [{ uid: "issue-graph-only", project_name: "Project A", status: "closed" }],
    });
    expect(projection.issues.map((item) => item.uid)).toEqual(["issue-authority"]);
    expect(projection.member_issue_uid_set.has("issue-graph-only")).toBe(false);
  });

  it("preserves the explicit graph root when graph enrichment is unavailable", () => {
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({
        graph_source_uid: "issue-root",
        enrichment: {
          errors: { graph: { code: "upstream_error", message: "Could not load reachable graph." } },
        },
      }),
    );

    expect(projection.graph_source_uid).toBe("issue-root");
    expect(projection.graph).toBeUndefined();
    expect(projection.enrichment_errors.graph).toEqual({
      code: "upstream_error",
      message: "Could not load reachable graph.",
    });
  });

  it("exposes enrichment errors as local data without rejecting authority", () => {
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({
        enrichment: {
          errors: {
            selected_history: { code: "history_unavailable", message: "History timed out" },
            graph: { code: "graph_unavailable", message: "Graph timed out" },
          },
        },
      }),
    );

    expect(projection.issues).toHaveLength(1);
    expect(projection.enrichment_errors).toEqual({
      selected_history: { code: "history_unavailable", message: "History timed out" },
      graph: { code: "graph_unavailable", message: "Graph timed out" },
    });
  });

  it.each([
    ["scope", { scope: "workspace", authority: "open" }],
    ["authority", { scope: "global", authority: "active" }],
    ["project scope without project UID", { scope: "project", authority: "open" }],
    ["project scope with padded project UID", { scope: "project", project_uid: " project-a ", authority: "open" }],
    ["global scope with project UID", { scope: "global", project_uid: "project-a", authority: "open" }],
    ["global scope with empty project UID", { scope: "global", project_uid: "", authority: "open" }],
  ])("rejects malformed authority intent: %s", (_label, intent) => {
    const raw = snapshot({ intent: intent as KataWorkspaceSnapshotResponse["intent"] });

    expect(() => normalizeKataWorkspaceSnapshot(raw)).toThrow(/invalid/i);
  });

  it("rejects a malformed authority issue status", () => {
    expect(() => normalizeKataWorkspaceSnapshot(snapshot({ issues: [issue({ status: "paused" })] }))).toThrow(
      /invalid/i,
    );
  });

  it("keeps authority data when selected detail normalization fails", () => {
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({
        enrichment: {
          selected_issue_uid: "issue-authority",
          selected_detail: {
            workspace_target: { available: false },
            detail: { issue: issue(), links: [{ type: "depends_on" }] },
          },
        },
      }),
    );

    expect(projection.issues).toHaveLength(1);
    expect(projection.selected_issue_uid).toBe("issue-authority");
    expect(projection.selected_detail).toBeUndefined();
    expect(projection.enrichment_errors.detail).toEqual({
      code: "invalid_snapshot_enrichment",
      message: "Could not normalize selected task detail.",
    });
  });

  it.each([
    ["depth", { source_uid: "issue-root", depth: "wide", hide_done: false, nodes: [], edges: [] }],
    [
      "edge",
      {
        source_uid: "issue-root",
        depth: "full",
        hide_done: false,
        nodes: [],
        edges: [{ from_uid: "issue-root", to_uid: "issue-authority", kind: "depends_on", layout: true }],
      },
    ],
    [
      "node",
      {
        source_uid: "issue-root",
        depth: "full",
        hide_done: false,
        nodes: [issue({ status: "paused" })],
        edges: [],
      },
    ],
  ])("keeps authority data when graph %s normalization fails", (_label, graph) => {
    const projection = normalizeKataWorkspaceSnapshot(
      snapshot({
        graph_source_uid: "issue-root",
        enrichment: { graph: graph as NonNullable<KataWorkspaceSnapshotResponse["enrichment"]["graph"]> },
      }),
    );

    expect(projection.issues).toHaveLength(1);
    expect(projection.graph_source_uid).toBe("issue-root");
    expect(projection.graph).toBeUndefined();
    expect(projection.enrichment_errors.graph).toEqual({
      code: "invalid_snapshot_enrichment",
      message: "Could not normalize reachable graph.",
    });
  });
});
