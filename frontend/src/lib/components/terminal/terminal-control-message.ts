import { Option, Schema } from "effect";

class TerminalExitedMessage extends Schema.Class<TerminalExitedMessage>("TerminalExitedMessage")({
  type: Schema.Literal("exited"),
  code: Schema.optionalKey(Schema.Number),
}) {}

class TerminalReplayReadyMessage extends Schema.Class<TerminalReplayReadyMessage>("TerminalReplayReadyMessage")({
  type: Schema.Literal("replay_ready"),
}) {}

class TerminalWorkspaceDeletedMessage extends Schema.Class<TerminalWorkspaceDeletedMessage>(
  "TerminalWorkspaceDeletedMessage",
)({
  type: Schema.Literal("workspace_deleted"),
}) {}

export type TerminalControlMessage =
  | TerminalExitedMessage
  | TerminalReplayReadyMessage
  | TerminalWorkspaceDeletedMessage;

const TerminalControlMessageSchema = Schema.Union([
  TerminalExitedMessage,
  TerminalReplayReadyMessage,
  TerminalWorkspaceDeletedMessage,
]);

export function decodeTerminalControlMessage(data: string): TerminalControlMessage | null {
  const decoded = Schema.decodeUnknownOption(Schema.fromJsonString(TerminalControlMessageSchema))(data);
  return Option.isSome(decoded) ? decoded.value : null;
}
