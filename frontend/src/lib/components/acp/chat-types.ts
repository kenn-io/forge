import { Schema } from "effect";

export const ChatMessageSchema = Schema.Struct({
  role: Schema.Literals(["user", "assistant", "tool"]),
  text: Schema.String,
  createdAt: Schema.optional(Schema.String),
  submissionId: Schema.optional(Schema.String),
  toolCallId: Schema.optional(Schema.String),
  status: Schema.optional(Schema.String),
});
export type ChatMessage = typeof ChatMessageSchema.Type;
const ConfigChoiceSchema = Schema.Struct({ value: Schema.String, name: Schema.String });
export const SessionConfigOptionSchema = Schema.Struct({
  id: Schema.String,
  name: Schema.String,
  description: Schema.optional(Schema.String),
  category: Schema.optional(Schema.String),
  type: Schema.String,
  currentValue: Schema.String,
  options: Schema.Array(
    Schema.Union([
      ConfigChoiceSchema,
      Schema.Struct({ group: Schema.String, name: Schema.String, options: Schema.Array(ConfigChoiceSchema) }),
    ]),
  ),
});
export type SessionConfigOption = typeof SessionConfigOptionSchema.Type;
export const ChatStateSchema = Schema.Struct({
  messages: Schema.Array(ChatMessageSchema),
  configOptions: Schema.Array(SessionConfigOptionSchema),
  configuring: Schema.Boolean,
  permissions: Schema.Array(
    Schema.Struct({
      id: Schema.String,
      title: Schema.String,
      options: Schema.Array(Schema.Struct({ optionId: Schema.String, name: Schema.String, kind: Schema.String })),
    }),
  ),
  busy: Schema.Boolean,
  connected: Schema.Boolean,
  error: Schema.String,
});
export type ChatState = typeof ChatStateSchema.Type;
export type ChatCommand =
  | { type: "prompt"; text: string; id: string }
  | { type: "cancel" }
  | { type: "config"; id: string; value: string }
  | { type: "permission"; id: string; optionId: string };
