import { assert, it } from "@effect/vitest";
import { Effect } from "effect";
import type { KataWorkspaceSnapshotResponse } from "../../api/kata/snapshot.js";
import { normalizeKataWorkspaceSnapshot } from "../../api/kata/snapshotProjection.js";
import {
  commentMutationEvidence,
  editMutationEvidence,
  issueCreateMutationEvidence,
  labelMutationEvidence,
  metadataMutationEvidence,
  moveMutationEvidence,
  ownerMutationEvidence,
  priorityMutationEvidence,
  projectCreateMutationEvidence,
  reconcileRecurrenceMutation,
  recurrenceCreateMatches,
  recurrencePatchMatches,
  statusMutationEvidence,
} from "./kata-mutation-evidence.js";
import type { KataRecurrence } from "../../api/kata/taskTypes.js";

function snapshot(): ReturnType<typeof normalizeKataWorkspaceSnapshot> {
  const issue = {
    id: 1,
    uid: "issue-1",
    project_id: 2,
    short_id: "1",
    qualified_id: "Roadmap#1",
    title: "Updated title",
    body: "Updated body",
    status: "closed" as const,
    project_uid: "project-roadmap",
    project_name: "Roadmap",
    metadata: { area: "work", checklist: [{ id: "one", text: "Ship", done: false }] },
    revision: 2,
    owner: "agent:new",
    author: "fixture-user",
    priority: 3,
    labels: ["urgent"],
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-04T00:00:00Z",
    closed_reason: "done" as const,
  };
  const response: KataWorkspaceSnapshotResponse = {
    server_instance_id: "server-a",
    daemon_id: "home",
    intent: { scope: "global", authority: "all" },
    generation: 2,
    invalidation_epoch: 2,
    event_cursor: 2,
    fetched_at: "2026-08-04T00:00:00Z",
    projects: [
      {
        id: 2,
        uid: "project-roadmap",
        name: "Roadmap",
        metadata: {},
        revision: 1,
        created_at: "2026-08-01T00:00:00Z",
        open_count: 0,
        closed_count: 1,
      },
    ],
    member_issue_uids: [issue.uid],
    issues: [issue],
    enrichment: {
      selected_issue_uid: issue.uid,
      selected_detail: {
        detail: {
          issue,
          comments: [
            {
              id: 4,
              issue_id: issue.id,
              author: "fixture-user",
              body: "Recorded",
              created_at: "2026-08-04T00:00:00Z",
            },
          ],
          labels: [
            {
              issue_id: issue.id,
              label: "urgent",
              author: "fixture-user",
              created_at: "2026-08-04T00:00:00Z",
            },
          ],
          links: [],
        },
        workspace_target: { available: false },
      },
      selected_history: [],
    },
  };
  return normalizeKataWorkspaceSnapshot(response);
}

it.effect("builds operation-specific Kata fence identities", () =>
  Effect.sync(() => {
    const evidence = labelMutationEvidence("issue-1", "urgent", true);
    assert.deepStrictEqual(evidence.identity("home"), {
      key: '["home","issue-label","issue-1:urgent"]',
      daemonId: "home",
      operation: "add Kata label",
      target: "issue-1:urgent",
    });
  }),
);

it.effect("requires positive snapshot evidence for each issue mutation family", () =>
  Effect.sync(() => {
    const fresh = snapshot();
    const evidence = [
      commentMutationEvidence("issue-1", "fixture-user", "Recorded", 0),
      editMutationEvidence("issue-1", { title: "Updated title", body: "Updated body" }),
      ownerMutationEvidence("issue-1", "agent:new"),
      priorityMutationEvidence("issue-1", 3),
      labelMutationEvidence("issue-1", "urgent", true),
      metadataMutationEvidence("issue-1", { area: "work" }),
      moveMutationEvidence("issue-1", "project-roadmap"),
      statusMutationEvidence("issue-1", "closed", "done"),
    ];

    for (const candidate of evidence) assert.isTrue(candidate.isApplied(fresh));
    assert.isTrue(metadataMutationEvidence("issue-1", { scheduled: null, due: null }).isApplied(fresh));
    assert.isFalse(ownerMutationEvidence("issue-1", undefined).isApplied(fresh));
    assert.isFalse(labelMutationEvidence("issue-1", "urgent", false).isApplied(fresh));
    assert.isFalse(statusMutationEvidence("issue-1", "open").isApplied(fresh));
  }),
);

it.effect("proves creates only from a unique new matching authority row", () =>
  Effect.sync(() => {
    const fresh = snapshot();
    const projectEvidence = projectCreateMutationEvidence("Roadmap", new Set(["project-existing"]));
    const issueEvidence = issueCreateMutationEvidence(
      "project-roadmap",
      { title: "Updated title", body: "Updated body" },
      new Set(["issue-existing"]),
    );

    assert.isTrue(projectEvidence.isApplied(fresh));
    assert.isTrue(issueEvidence.isApplied(fresh));
    assert.isFalse(projectCreateMutationEvidence("Roadmap", new Set(["project-roadmap"])).isApplied(fresh));
    assert.isFalse(issueCreateMutationEvidence("project-roadmap", { title: "Other" }, new Set()).isApplied(fresh));
  }),
);

it.effect("matches recurrence creation and patch evidence against authoritative recurrence rows", () =>
  Effect.sync(() => {
    const recurrence: KataRecurrence = {
      id: 1,
      uid: "recurrence-1",
      project_id: 2,
      rrule: "FREQ=WEEKLY",
      dtstart: "2026-08-04",
      timezone: "UTC",
      template_title: "Review backlog",
      template_body: "",
      template_owner: "agent:new",
      template_priority: 2,
      template_labels: ["weekly"],
      template_metadata: { area: "work" },
      author: "fixture-user",
      revision: 2,
      created_at: "2026-08-01T00:00:00Z",
      updated_at: "2026-08-04T00:00:00Z",
    };

    assert.isTrue(
      recurrenceCreateMatches(recurrence, {
        actor: "fixture-user",
        rrule: "FREQ=WEEKLY",
        dtstart: "2026-08-04",
        timezone: "UTC",
        template: {
          title: "Review backlog",
          body: "",
          owner: "agent:new",
          priority: 2,
          labels: ["weekly"],
          metadata: { area: "work" },
        },
      }),
    );
    assert.isTrue(recurrencePatchMatches(recurrence, { actor: "fixture-user", timezone: "UTC" }));
    assert.isFalse(recurrencePatchMatches(recurrence, { actor: "fixture-user", timezone: "America/New_York" }));
  }),
);

it.effect("recognizes a lost-response recurrence clear from normalized authority", () =>
  Effect.gen(function* () {
    const baseline: KataRecurrence = {
      id: 1,
      uid: "recurrence-1",
      project_id: 2,
      rrule: "FREQ=WEEKLY",
      dtstart: "2026-08-04",
      timezone: "UTC",
      template_title: "Review backlog",
      template_body: "",
      template_owner: "agent:new",
      template_priority: 2,
      template_labels: [],
      template_metadata: {},
      author: "fixture-user",
      revision: 2,
      created_at: "2026-08-01T00:00:00Z",
      updated_at: "2026-08-04T00:00:00Z",
    };
    const cleared: KataRecurrence = {
      ...baseline,
      template_owner: undefined,
      template_priority: undefined,
      revision: 3,
    };
    const patch = {
      actor: "fixture-user",
      template: { owner: null, priority: null },
    } as const;

    const resolution = yield* reconcileRecurrenceMutation(
      [baseline],
      Effect.succeed({ recurrences: [cleared], fetched_at: "2026-08-04T00:01:00Z" }),
      (recurrences) => recurrencePatchMatches(recurrences[0]!, patch),
    );

    assert.strictEqual(resolution, "applied");
  }),
);
