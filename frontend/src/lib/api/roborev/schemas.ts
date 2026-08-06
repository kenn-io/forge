import { Schema } from "effect";

export class RoborevEvent extends Schema.Class<RoborevEvent>("RoborevEvent")({
  type: Schema.String,
  ts: Schema.String,
  job_id: Schema.Number,
  repo: Schema.String,
  repo_name: Schema.String,
  sha: Schema.String,
  job_uuid: Schema.optionalKey(Schema.String),
  branch: Schema.optionalKey(Schema.String),
  agent: Schema.optionalKey(Schema.String),
  verdict: Schema.optionalKey(Schema.String),
  findings: Schema.optionalKey(Schema.String),
  error: Schema.optionalKey(Schema.String),
  worktree_path: Schema.optionalKey(Schema.String),
}) {}

export class RoborevStreamOpened extends Schema.TaggedClass<RoborevStreamOpened>()("RoborevStreamOpened", {
  opened: Schema.Boolean,
}) {}

export class RoborevLogLinePayload extends Schema.Class<RoborevLogLinePayload>("RoborevLogLinePayload")({
  ts: Schema.optionalKey(Schema.String),
  text: Schema.optionalKey(Schema.String),
  line_type: Schema.optionalKey(Schema.String),
}) {}

export class RoborevJobOutputSnapshot extends Schema.Class<RoborevJobOutputSnapshot>("RoborevJobOutputSnapshot")({
  lines: Schema.optionalKey(Schema.NullOr(Schema.Array(RoborevLogLinePayload))),
}) {}
