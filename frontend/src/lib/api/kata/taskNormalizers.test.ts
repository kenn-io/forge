import { describe, expect, test } from "vite-plus/test";

import {
  normalizeKataEvents,
  normalizeKataInstance,
  normalizeKataProjectList,
  normalizeKataRecurrences,
  normalizeKataTaskDetail,
  normalizeKataTaskList,
} from "./taskNormalizers.js";

describe("kata task normalizers", () => {
  test("normalizes instance responses through the shared normalizer module", () => {
    expect(
      normalizeKataInstance({
        body: {
          instance_uid: "01KATAMOCKINSTANCE001",
          version: "dev",
          schema_version: 10,
        },
      }),
    ).toEqual({
      instance_uid: "01KATAMOCKINSTANCE001",
      version: "dev",
      schema_version: 10,
    });

    expect(normalizeKataInstance({ body: { instance_uid: 42, version: null, schema_version: "10" } })).toEqual({
      instance_uid: "",
      version: "",
      schema_version: 0,
    });
  });

  test("normalizes nullable issue detail arrays and show-response label rows", () => {
    const detail = normalizeKataTaskDetail({
      issue: {
        id: 101,
        uid: "issue-pay-rent",
        project_id: 2,
        project_uid: "project-finances",
        project_name: "Finances",
        short_id: "rent",
        qualified_id: "Finances#rent",
        title: "Pay rent",
        body: "Due by month start.",
        status: "open",
        author: "fixture-user",
        metadata: '{"scheduled_on":"2026-05-15","custom":true}',
        revision: 4,
        created_at: "2026-04-28T12:00:00.000Z",
        updated_at: "2026-05-15T16:00:00.000Z",
      },
      comments: null,
      links: null,
      labels: [
        { issue_id: 101, label: "money", author: "fixture-user", created_at: "2026-04-28T12:02:00.000Z" },
        { issue_id: 101, label: "monthly", author: "fixture-user", created_at: "2026-04-28T12:03:00.000Z" },
      ],
    });

    expect(detail.comments).toEqual([]);
    expect(detail.links).toEqual([]);
    expect(detail.labels.map((label) => label.label)).toEqual(["money", "monthly"]);
    expect(detail.issue.labels).toEqual(["money", "monthly"]);
    expect(detail.issue.metadata).toEqual({ scheduled_on: "2026-05-15", custom: true });
  });

  test("maps project stats to sidebar open counts and parses project metadata", () => {
    const projects = normalizeKataProjectList({
      projects: [
        {
          id: 2,
          uid: "project-finances",
          name: "Finances",
          metadata: '{"area":"Personal","sidebar_order":10,"unknown":"kept"}',
          revision: 3,
          created_at: "2026-05-01T12:00:00.000Z",
          stats: { open: 7, closed: 2, last_event_at: "2026-05-14T09:00:00.000Z" },
        },
      ],
      fetched_at: "2026-05-15T16:00:00.000Z",
    });

    expect(projects.projects[0]).toMatchObject({
      uid: "project-finances",
      open_count: 7,
      metadata: { area: "Personal", sidebar_order: 10, unknown: "kept" },
      revision: 3,
      created_at: "2026-05-01T12:00:00.000Z",
    });
    expect(projects.fetched_at).toBe("2026-05-15T16:00:00.000Z");
  });

  test("preserves issue identity, relationship, metadata, recurrence, and date fields", () => {
    const response = normalizeKataTaskList(
      {
        issues: [
          {
            id: 101,
            uid: "issue-pay-rent",
            project_id: 2,
            project_uid: "project-finances",
            project_name: "Finances",
            short_id: "rent",
            qualified_id: "Finances#rent",
            title: "Pay rent",
            body: "Due by month start.",
            status: "open",
            owner: "fixture-user",
            author: "agent",
            priority: 0,
            labels: ["money", "monthly"],
            parent_short_id: "home",
            child_counts: { open: 1, total: 2 },
            blocks: [{ uid: "issue-late-fee", short_id: "late" }],
            blocked_by: [{ uid: "issue-cash", short_id: "cash" }],
            related: [{ uid: "issue-lease", short_id: "lease" }],
            metadata: '{"deadline_on":"2026-05-01","nested":{"x":1}}',
            revision: 4,
            recurrence_id: 8,
            occurrence_key: "2026-05-15",
            created_at: "2026-04-28T12:00:00.000Z",
            updated_at: "2026-05-15T16:00:00.000Z",
            closed_reason: "done",
            closed_at: "2026-05-16T10:00:00.000Z",
            deleted_at: "2026-05-17T10:00:00.000Z",
          },
        ],
      },
      { view: "today" },
    );

    const issue = response.groups[0]!.issues[0]!;
    expect(response.view).toBe("today");
    expect(issue).toMatchObject({
      qualified_id: "Finances#rent",
      short_id: "rent",
      uid: "issue-pay-rent",
      owner: "fixture-user",
      priority: 0,
      labels: ["money", "monthly"],
      blocks: [{ uid: "issue-late-fee", short_id: "late" }],
      blocked_by: [{ uid: "issue-cash", short_id: "cash" }],
      related: [{ uid: "issue-lease", short_id: "lease" }],
      metadata: { deadline_on: "2026-05-01", nested: { x: 1 } },
      revision: 4,
      project_uid: "project-finances",
      project_name: "Finances",
      recurrence_id: 8,
      occurrence_key: "2026-05-15",
      created_at: "2026-04-28T12:00:00.000Z",
      updated_at: "2026-05-15T16:00:00.000Z",
      closed_reason: "done",
      closed_at: "2026-05-16T10:00:00.000Z",
      deleted_at: "2026-05-17T10:00:00.000Z",
    });
  });

  test("falls back to empty metadata objects for absent, empty, and invalid metadata strings", () => {
    expect(normalizeKataTaskList({ issues: [{ metadata: "", labels: null }] }).groups[0]!.issues[0]!.metadata).toEqual(
      {},
    );
    expect(normalizeKataTaskDetail({ issue: { metadata: "{broken" } }).issue.metadata).toEqual({});
    expect(normalizeKataProjectList({ projects: [{ metadata: null }] }).projects[0]!.metadata).toEqual({});
  });

  test("sanitizes known metadata field types while preserving unknown extension keys", () => {
    const issue = normalizeKataTaskList({
      issues: [
        {
          metadata: JSON.stringify({
            scheduled_on: 123,
            deadline_on: "2026-05-20",
            today_bucket: "lunch",
            checklist: [
              { id: "01HZX4Y5Z6A7B8C9D0E1F2G3H7", text: "Draft", done: false },
              { id: 42, text: "bad", done: false },
            ],
            custom_shape: { preserved: true },
          }),
        },
      ],
    }).groups[0]!.issues[0]!;
    const project = normalizeKataProjectList({
      projects: [
        {
          metadata: JSON.stringify({
            area: 123,
            sidebar_order: "first",
            icon: "folder",
            timezone: "America/Chicago",
            color: "blue",
          }),
        },
      ],
    }).projects[0]!;

    expect(issue.metadata).toEqual({
      deadline_on: "2026-05-20",
      checklist: [{ id: "01HZX4Y5Z6A7B8C9D0E1F2G3H7", text: "Draft", done: false }],
      custom_shape: { preserved: true },
    });
    expect(project.metadata).toEqual({
      icon: "folder",
      timezone: "America/Chicago",
      color: "blue",
    });
  });

  test("preserves an empty checklist array in issue metadata", () => {
    const issue = normalizeKataTaskDetail({
      issue: { metadata: JSON.stringify({ checklist: [] }) },
    }).issue;

    expect(issue.metadata.checklist).toEqual([]);
  });

  test("normalizes recurrence rows with JSON template labels and metadata", () => {
    const response = normalizeKataRecurrences({
      recurrences: [
        {
          id: 1,
          uid: "01HZX4Y5Z6A7B8C9D0E1F2G3H4",
          project_id: 2,
          rrule: "FREQ=WEEKLY;COUNT=2",
          dtstart: "2026-05-20",
          timezone: "America/Chicago",
          template_title: "Weekly review",
          template_body: "Review open loops.",
          template_owner: "agent:planner",
          template_priority: 2,
          template_labels: '["routine","weekly"]',
          template_metadata: '{"checklist":[]}',
          next_occurrence_key: "2026-05-27",
          last_materialized_uid: "issue-last",
          author: "fixture-user",
          revision: 3,
          created_at: "2026-05-15T12:00:00.000Z",
          updated_at: "2026-05-15T12:30:00.000Z",
        },
      ],
    });

    expect(response.recurrences).toEqual([
      expect.objectContaining({
        uid: "01HZX4Y5Z6A7B8C9D0E1F2G3H4",
        rrule: "FREQ=WEEKLY;COUNT=2",
        template_labels: ["routine", "weekly"],
        template_metadata: { checklist: [] },
        next_occurrence_key: "2026-05-27",
        last_materialized_uid: "issue-last",
      }),
    ]);
  });

  test("normalizes event payload JSON strings and preserves hub cursor fields", () => {
    const response = normalizeKataEvents({
      reset_required: false,
      reset_after_id: 4,
      events: [
        {
          event_id: 5,
          event_uid: "event-links-pay-rent",
          origin_instance_uid: "01KATAMOCKINSTANCE001",
          type: "issue.links_changed",
          project_id: 2,
          project_uid: "project-finances",
          project_name: "Finances",
          issue_id: 101,
          issue_uid: "issue-pay-rent",
          issue_short_id: "rent",
          actor: "fixture-user",
          payload: '{"blocks_added":[{"uid":"issue-late-fee","short_id":"late"}]}',
          created_at: "2026-05-14T09:12:00.000Z",
        },
      ],
      next_after_id: 5,
    });

    expect(response.events[0]).toMatchObject({
      event_id: 5,
      event_uid: "event-links-pay-rent",
      origin_instance_uid: "01KATAMOCKINSTANCE001",
      payload: { blocks_added: [{ uid: "issue-late-fee", short_id: "late" }] },
    });
    expect(response).toMatchObject({
      reset_required: false,
      reset_after_id: 4,
      next_after_id: 5,
    });
  });
});
