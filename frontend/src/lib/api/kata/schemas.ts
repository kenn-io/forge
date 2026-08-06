import { Schema } from "effect";

export class KataTaskEventStreamFrame extends Schema.Class<KataTaskEventStreamFrame>("KataTaskEventStreamFrame")({
  kind: Schema.Union([Schema.Literal("reset"), Schema.Literal("invalidation")]),
  server_instance_id: Schema.NonEmptyString,
  daemon_id: Schema.NonEmptyString,
  epoch: Schema.Natural,
  cursor: Schema.Natural,
}) {}

export class KataEventStreamOpened extends Schema.Class<KataEventStreamOpened>("KataEventStreamOpened")({
  opened: Schema.Literal(true),
}) {}

export type KataEventStreamEvent = KataEventStreamOpened | KataTaskEventStreamFrame;
