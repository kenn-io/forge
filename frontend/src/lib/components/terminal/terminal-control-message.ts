import { Option, Schema } from "effect";

class TerminalExitedMessage extends Schema.Class<TerminalExitedMessage>("TerminalExitedMessage")({
  type: Schema.Literal("exited"),
  code: Schema.optionalKey(Schema.Number),
}) {}

class TerminalReplayReadyMessage extends Schema.Class<TerminalReplayReadyMessage>("TerminalReplayReadyMessage")({
  type: Schema.Literal("replay_ready"),
}) {}

export type TerminalControlMessage = TerminalExitedMessage | TerminalReplayReadyMessage;

const TerminalControlMessageSchema = Schema.Union([TerminalExitedMessage, TerminalReplayReadyMessage]);

export function decodeTerminalControlMessage(data: string): TerminalControlMessage | null {
  const decoded = Schema.decodeUnknownOption(Schema.fromJsonString(TerminalControlMessageSchema))(data);
  return Option.isSome(decoded) ? decoded.value : null;
}
