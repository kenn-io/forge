import { Schema } from "effect";

export const ChatMessageSchema = Schema.Struct({
  role: Schema.Literals(["user", "assistant", "tool"]),
  text: Schema.String,
  createdAt: Schema.optional(Schema.String),
  submissionId: Schema.optional(Schema.String),
  toolCallId: Schema.optional(Schema.String),
  status: Schema.optional(Schema.String),
  // A tool call that runs a delegated sub-agent, and the sub-agent tool call
  // that a nested tool call was made inside.
  subagent: Schema.optional(Schema.NullOr(Schema.Boolean)),
  parentToolCallId: Schema.optional(Schema.NullOr(Schema.String)),
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
// Go encodes nil maps and slices as null, so optional collections accept it.
export const ElicitationSchema = Schema.Struct({
  id: Schema.String,
  message: Schema.String,
  schema: Schema.Struct({
    title: Schema.optional(Schema.NullOr(Schema.String)),
    description: Schema.optional(Schema.NullOr(Schema.String)),
    properties: Schema.NullOr(Schema.Record(Schema.String, Schema.Unknown)),
    required: Schema.optional(Schema.NullOr(Schema.Array(Schema.String))),
  }),
});
export type Elicitation = typeof ElicitationSchema.Type;
export const AgentCommandSchema = Schema.Struct({
  name: Schema.String,
  description: Schema.String,
  inputHint: Schema.optional(Schema.NullOr(Schema.String)),
});
export type AgentCommand = typeof AgentCommandSchema.Type;
export const ChatStateSchema = Schema.Struct({
  messages: Schema.Array(ChatMessageSchema),
  configOptions: Schema.Array(SessionConfigOptionSchema),
  configuring: Schema.Boolean,
  historyTruncated: Schema.Boolean,
  permissions: Schema.Array(
    Schema.Struct({
      id: Schema.String,
      title: Schema.String,
      options: Schema.Array(Schema.Struct({ optionId: Schema.String, name: Schema.String, kind: Schema.String })),
    }),
  ),
  // Absent or null from daemons that predate elicitation, slash commands,
  // and prompt queueing/steering.
  elicitations: Schema.optional(Schema.NullOr(Schema.Array(ElicitationSchema))),
  commands: Schema.optional(Schema.NullOr(Schema.Array(AgentCommandSchema))),
  queue: Schema.optional(Schema.NullOr(Schema.Array(Schema.Struct({ id: Schema.String, text: Schema.String })))),
  queuePaused: Schema.optional(Schema.NullOr(Schema.Boolean)),
  steeringSupported: Schema.optional(Schema.NullOr(Schema.Boolean)),
  steering: Schema.optional(Schema.NullOr(Schema.Boolean)),
  busy: Schema.Boolean,
  connected: Schema.Boolean,
  error: Schema.String,
});
export type ChatState = typeof ChatStateSchema.Type;
export type PromptMode = "send" | "queue" | "steer";
export type ChatCommand =
  | { type: "prompt"; mode: PromptMode; text: string; id: string }
  | { type: "unqueue"; id: string }
  | { type: "resume" }
  | { type: "cancel" }
  | { type: "config"; id: string; value: string }
  | { type: "permission"; id: string; optionId: string }
  | { type: "elicitation"; id: string; action: "accept"; content: Record<string, unknown> }
  | { type: "elicitation"; id: string; action: "decline" | "cancel" };
