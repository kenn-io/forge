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
export const ChatStateSchema = Schema.Struct({
  messages: Schema.Array(ChatMessageSchema),
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
  | { type: "permission"; id: string; optionId: string };
