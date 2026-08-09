import { assert, describe, it } from "@effect/vitest";
import { Deferred, Effect, Ref } from "effect";
import type { WorkspaceItemIdentity } from "../../workspace-inline.js";
import {
  discardWorkspaceLaunch,
  resetWorkspaceCreatePendingForTest,
} from "../../stores/workspace-create-pending.svelte.js";
import type { KataWorkspaceResponse } from "../../api/kata/workspaces.js";
import {
  makeKataWorkspaceCreationWorkflow,
  type KataWorkspaceCreationPort,
  type KataWorkspaceCreationRequest,
} from "./kata-workspace-creation-workflow.js";

const itemIdentity: WorkspaceItemIdentity = {
  provider: "github",
  platformHost: "github.com",
  owner: "acme",
  name: "widget",
  repoPath: "acme/widget",
  number: 0,
  itemType: "kata_task",
};

const request: KataWorkspaceCreationRequest = {
  purpose: {
    daemon_id: "home",
    project_uid: "project-1",
    issue_uid: "issue-1",
  },
  itemIdentity,
  launchTargetKey: "codex",
  presentation: {
    isCurrent: () => true,
    navigate: () => undefined,
  },
};

function workspace(status: string): KataWorkspaceResponse {
  return {
    id: "ws-1",
    created_at: "2026-08-09T00:00:00Z",
    enrichment_status: "not_applicable",
    git_head_ref: "kenn-forge/kata/issue-1",
    item_key: "kata:home:project-1:issue-1",
    item_number: 0,
    item_type: "kata_task",
    kata: {
      daemon_id: "home",
      project_uid: "project-1",
      issue_uid: "issue-1",
    },
    platform_host: "github.com",
    repo: {
      provider: "github",
      platform_host: "github.com",
      repo_path: "acme/widget",
      owner: "acme",
      name: "widget",
      capabilities: {
        read_repositories: true,
        read_merge_requests: true,
        read_issues: true,
        read_comments: true,
        read_releases: true,
        read_ci: true,
        read_labels: true,
        read_markdown_images: true,
        read_authenticated_user: true,
        comment_mutation: true,
        state_mutation: true,
        merge_mutation: true,
        review_mutation: true,
        workflow_approval: true,
        ready_for_review: true,
        draft_mutation: true,
        issue_mutation: true,
        label_mutation: true,
        assignee_mutation: true,
        reviewer_mutation: true,
        thread_reply: true,
        thread_resolve: true,
        review_draft_mutation: true,
        review_thread_resolution: true,
        review_suggestion_application: true,
        read_review_threads: true,
        native_multiline_ranges: true,
        mutation_head_binding: true,
        supported_review_actions: [],
      },
    },
    repo_name: "widget",
    repo_owner: "acme",
    status,
    tmux_activity_source: "unknown",
    tmux_last_output_at: null,
    tmux_session: "forge-ws-1",
    tmux_working: false,
    worktree_path: "/tmp/ws-1",
  };
}

describe("KataWorkspaceCreationWorkflow", () => {
  it.effect("owns the accepted create after the submitting surface releases its command", () =>
    Effect.scoped(
      Effect.gen(function* () {
        resetWorkspaceCreatePendingForTest();
        yield* Effect.addFinalizer(() => Effect.sync(resetWorkspaceCreatePendingForTest));
        const releaseCreate = yield* Deferred.make<void>();
        const completed = yield* Deferred.make<void>();
        const port: KataWorkspaceCreationPort = {
          create: () => Deferred.await(releaseCreate).pipe(Effect.andThen(Deferred.succeed(completed, undefined))),
          load: () => Effect.succeed(workspace("creating")),
          list: () => Effect.succeed([]),
        };
        const workflow = yield* makeKataWorkspaceCreationWorkflow(port);

        yield* workflow.submit(request);
        yield* Deferred.succeed(releaseCreate, undefined);

        yield* Deferred.await(completed);
      }),
    ),
  );

  it.effect("retains launch intent until workspace status is ready", () =>
    Effect.scoped(
      Effect.gen(function* () {
        resetWorkspaceCreatePendingForTest();
        yield* Effect.addFinalizer(() => Effect.sync(resetWorkspaceCreatePendingForTest));
        const status = yield* Ref.make("creating");
        const navigated = yield* Ref.make<readonly string[]>([]);
        const port: KataWorkspaceCreationPort = {
          create: () => Effect.void,
          load: () => Ref.get(status).pipe(Effect.map(workspace)),
          list: () => Effect.succeed([]),
        };
        const workflow = yield* makeKataWorkspaceCreationWorkflow(port);
        yield* workflow.submit({
          ...request,
          presentation: {
            isCurrent: () => true,
            navigate: (id) => Ref.update(navigated, (ids) => [...ids, id]),
          },
        });

        yield* workflow.workspaceCreated("ws-1");
        assert.deepStrictEqual(yield* Ref.get(navigated), ["ws-1"]);
        assert.isNull(discardWorkspaceLaunch("ws-1", undefined));

        yield* Ref.set(status, "ready");
        yield* workflow.workspaceStatus("ws-1");
        assert.strictEqual(discardWorkspaceLaunch("ws-1", undefined), "codex");
      }),
    ),
  );

  it.effect("reconciles a missed creation without navigating a stale surface", () =>
    Effect.scoped(
      Effect.gen(function* () {
        resetWorkspaceCreatePendingForTest();
        yield* Effect.addFinalizer(() => Effect.sync(resetWorkspaceCreatePendingForTest));
        const navigated = yield* Ref.make(0);
        const notices = yield* Ref.make<readonly string[]>([]);
        const port: KataWorkspaceCreationPort = {
          create: () => Effect.void,
          load: () => Effect.succeed(workspace("ready")),
          list: () => Effect.succeed([workspace("ready")]),
        };
        const workflow = yield* makeKataWorkspaceCreationWorkflow(port, {
          notify: (message) => Ref.update(notices, (current) => [...current, message]),
        });
        yield* workflow.submit({
          ...request,
          presentation: {
            isCurrent: () => false,
            navigate: () => Ref.update(navigated, (count) => count + 1),
          },
        });

        yield* workflow.reconcile;

        assert.strictEqual(yield* Ref.get(navigated), 0);
        assert.deepStrictEqual(yield* Ref.get(notices), ["Workspace created."]);
        assert.strictEqual(discardWorkspaceLaunch("ws-1", undefined), "codex");
      }),
    ),
  );

  it.effect("ignores unrelated status events when no creation is pending", () =>
    Effect.scoped(
      Effect.gen(function* () {
        const loads = yield* Ref.make(0);
        const port: KataWorkspaceCreationPort = {
          create: () => Effect.void,
          load: () => Ref.update(loads, (count) => count + 1).pipe(Effect.as(workspace("ready"))),
          list: () => Effect.succeed([]),
        };
        const workflow = yield* makeKataWorkspaceCreationWorkflow(port);

        yield* workflow.workspaceStatus("unrelated");

        assert.strictEqual(yield* Ref.get(loads), 0);
      }),
    ),
  );
});
