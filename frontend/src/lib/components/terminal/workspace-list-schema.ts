import { Effect, Schema } from "effect";
import type { components } from "../../api/generated/schema.js";
import { InvalidExternalPayload } from "../../api/effect-errors.js";

type GeneratedWorkspace = components["schemas"]["WorkspaceResponse"];
type GeneratedRepo = components["schemas"]["RepoRefResponse"];

export type WorkspaceListItem = Pick<
  GeneratedWorkspace,
  | "created_at"
  | "git_head_ref"
  | "id"
  | "item_number"
  | "item_type"
  | "platform_host"
  | "repo_name"
  | "repo_owner"
  | "status"
  | "tmux_activity_source"
  | "tmux_last_output_at"
  | "tmux_working"
  | "worktree_path"
> &
  Partial<Pick<GeneratedWorkspace, "item_key" | "kata">> & {
    readonly agent_state?: GeneratedWorkspace["agent_state"] | null;
    readonly agent_state_updated_at?: string | null;
    readonly associated_pr_number?: Exclude<GeneratedWorkspace["associated_pr_number"], undefined> | null;
    readonly commits_ahead?: number | null;
    readonly commits_behind?: number | null;
    readonly error_message?: GeneratedWorkspace["error_message"] | null;
    readonly item_last_activity_at?: string | null;
    readonly mr_additions?: number | null;
    readonly mr_deletions?: number | null;
    readonly mr_is_draft?: boolean | null;
    readonly mr_state?: string | null;
    readonly mr_title?: string | null;
    readonly repo?: Pick<
      GeneratedRepo,
      "name" | "owner" | "platform_host" | "platform_repo_id" | "provider" | "repo_path"
    >;
    readonly tmux_pane_title?: string | null;
    readonly fleet_host_key?: string;
    readonly fleet_host_name?: string;
  };

const Repo = Schema.Struct({
  name: Schema.String,
  owner: Schema.String,
  platform_host: Schema.String,
  platform_repo_id: Schema.optionalKey(Schema.String),
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

const Workspace = Schema.Struct({
  agent_state: Schema.optionalKey(Schema.NullOr(Schema.Literals(["idle", "working", "input", "approval", "done"]))),
  agent_state_updated_at: Schema.optionalKey(Schema.NullOr(Schema.String)),
  associated_pr_number: Schema.optionalKey(Schema.NullOr(Schema.Number)),
  commits_ahead: Schema.optionalKey(Schema.NullOr(Schema.Number)),
  commits_behind: Schema.optionalKey(Schema.NullOr(Schema.Number)),
  created_at: Schema.String,
  error_message: Schema.optionalKey(Schema.NullOr(Schema.String)),
  git_head_ref: Schema.String,
  id: Schema.String,
  item_key: Schema.optionalKey(Schema.String),
  item_last_activity_at: Schema.optionalKey(Schema.NullOr(Schema.String)),
  item_number: Schema.Number,
  item_type: Schema.String,
  kata: Schema.optionalKey(Kata),
  mr_additions: Schema.optionalKey(Schema.NullOr(Schema.Number)),
  mr_deletions: Schema.optionalKey(Schema.NullOr(Schema.Number)),
  mr_is_draft: Schema.optionalKey(Schema.NullOr(Schema.Boolean)),
  mr_state: Schema.optionalKey(Schema.NullOr(Schema.String)),
  mr_title: Schema.optionalKey(Schema.NullOr(Schema.String)),
  platform_host: Schema.String,
  repo: Schema.optionalKey(Repo),
  repo_name: Schema.String,
  repo_owner: Schema.String,
  status: Schema.String,
  tmux_activity_source: Schema.String,
  tmux_last_output_at: Schema.NullOr(Schema.String),
  tmux_pane_title: Schema.optionalKey(Schema.NullOr(Schema.String)),
  tmux_working: Schema.Boolean,
  worktree_path: Schema.String,
});

const WorkspaceList = Schema.Struct({
  workspaces: Schema.NullOr(Schema.Array(Workspace)),
});

export const decodeWorkspaceList = Effect.fn("WorkspaceList.decode")(function* (input: unknown) {
  const decoded = yield* Schema.decodeUnknownEffect(WorkspaceList)(input).pipe(
    Effect.mapError((cause) =>
      InvalidExternalPayload.make({
        operation: "decode fleet workspace list",
        cause,
      }),
    ),
  );
  const workspaces: readonly WorkspaceListItem[] = decoded.workspaces ?? [];
  return workspaces;
});
