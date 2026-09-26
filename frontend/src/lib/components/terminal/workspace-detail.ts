import { Effect, Schema } from "effect";
import type { RepoRefResponse, WorkspaceResponse } from "../../api/generated/models/index.js";
import { InvalidExternalPayload } from "../../api/effect-errors.js";

type GeneratedWorkspace = WorkspaceResponse;
type GeneratedRepo = RepoRefResponse;

export type WorkspaceDetail = Pick<
  GeneratedWorkspace,
  | "created_at"
  | "enrichment_status"
  | "git_head_ref"
  | "id"
  | "item_number"
  | "platform_host"
  | "repo_name"
  | "repo_owner"
  | "status"
  | "tmux_session"
  | "worktree_path"
> &
  Partial<
    Pick<
      GeneratedWorkspace,
      | "item_key"
      | "kata"
      | "commit_attribution"
      | "worktree_dirty"
      | "commits_ahead"
      | "commits_vs_pr_head"
      | "branch_upstream_missing"
    >
  > & {
    readonly associated_pr_number?: Exclude<GeneratedWorkspace["associated_pr_number"], undefined> | null;
    readonly error_message?: Exclude<GeneratedWorkspace["error_message"], undefined> | null;
    readonly item_type: "pull_request" | "issue" | "kata_task" | "adhoc";
    readonly mr_head_repo_kind?: Exclude<GeneratedWorkspace["mr_head_repo_kind"], undefined> | null;
    readonly mr_is_draft?: Exclude<GeneratedWorkspace["mr_is_draft"], undefined> | null;
    readonly mr_state?: Exclude<GeneratedWorkspace["mr_state"], undefined> | null;
    readonly mr_title?: Exclude<GeneratedWorkspace["mr_title"], undefined> | null;
    readonly repo: Pick<
      GeneratedRepo,
      "name" | "owner" | "platform_host" | "platform_repo_id" | "provider" | "repo_path"
    >;
    readonly fleet_host_key?: string | undefined;
  };

const Repo = Schema.Struct({
  name: Schema.String,
  owner: Schema.String,
  platform_host: Schema.String,
  platform_repo_id: Schema.optionalKey(Schema.Number),
  provider: Schema.String,
  repo_path: Schema.String,
});

const Kata = Schema.Struct({
  daemon_id: Schema.String,
  issue_uid: Schema.String,
  project_name: Schema.optionalKey(Schema.String),
  project_uid: Schema.String,
  qualified_id: Schema.optionalKey(Schema.String),
  short_id: Schema.optionalKey(Schema.String),
  title: Schema.optionalKey(Schema.String),
});

const Attribution = Schema.Struct({
  repository: Schema.String,
  branch: Schema.String,
  oid: Schema.String,
  pushed: Schema.Boolean,
  status: Schema.Literals(["matched", "preserved_author", "mismatch", "unverified"]),
  message: Schema.String,
  expected_github_user_id: Schema.Number,
  author_id: Schema.Number,
  committer_id: Schema.Number,
  author_name: Schema.String,
  author_email: Schema.String,
  committer_name: Schema.String,
  committer_email: Schema.String,
});

const WorkspaceDetailSchema = Schema.Struct({
  commit_attribution: Schema.optionalKey(Attribution),
  associated_pr_number: Schema.optionalKey(Schema.NullOr(Schema.Number)),
  branch_upstream_missing: Schema.optionalKey(Schema.Boolean),
  commits_ahead: Schema.optionalKey(Schema.Number),
  commits_vs_pr_head: Schema.optionalKey(Schema.Boolean),
  created_at: Schema.String,
  enrichment_status: Schema.Literals(["not_applicable", "pending", "fresh", "stale", "failed"]),
  error_message: Schema.optionalKey(Schema.NullOr(Schema.String)),
  git_head_ref: Schema.String,
  id: Schema.String,
  item_key: Schema.optionalKey(Schema.String),
  item_number: Schema.Number,
  item_type: Schema.Literals(["pull_request", "issue", "kata_task", "adhoc"]),
  kata: Schema.optionalKey(Kata),
  mr_head_repo_kind: Schema.optionalKey(Schema.NullOr(Schema.Literals(["same_repo", "fork", "unknown"]))),
  mr_is_draft: Schema.optionalKey(Schema.NullOr(Schema.Boolean)),
  mr_state: Schema.optionalKey(Schema.NullOr(Schema.String)),
  mr_title: Schema.optionalKey(Schema.NullOr(Schema.String)),
  platform_host: Schema.String,
  repo: Repo,
  repo_name: Schema.String,
  repo_owner: Schema.String,
  status: Schema.String,
  tmux_session: Schema.String,
  worktree_dirty: Schema.optionalKey(Schema.Boolean),
  worktree_path: Schema.String,
});

export const decodeWorkspaceDetail = Effect.fn("WorkspaceDetail.decode")(function* (input: unknown, hostKey?: string) {
  const workspace: WorkspaceDetail = yield* Schema.decodeUnknownEffect(WorkspaceDetailSchema)(input).pipe(
    Effect.mapError((cause) =>
      InvalidExternalPayload.make({
        operation: "decode workspace detail",
        cause,
      }),
    ),
  );
  return hostKey === undefined ? workspace : { ...workspace, fleet_host_key: hostKey };
});
