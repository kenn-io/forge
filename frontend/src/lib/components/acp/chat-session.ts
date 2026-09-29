import { Option, Schema } from "effect";
import { makeTerminalSessionController } from "../terminal/terminal-session.js";
import { ChatStateSchema, type ChatCommand, type ChatMessage, type ChatState } from "./chat-types.js";

const decodeFrame = Schema.decodeUnknownOption(
  Schema.fromJsonString(Schema.Union([ChatStateSchema, Schema.Struct({ commandError: Schema.String })])),
);

// Structural equality for decoded JSON values (messages nest content blocks,
// tool content, and locations).
function sameValue(left: unknown, right: unknown): boolean {
  if (left === right) return true;
  if (typeof left !== "object" || typeof right !== "object" || left === null || right === null) return false;
  if (Array.isArray(left) !== Array.isArray(right)) return false;
  const a = left as Record<string, unknown>;
  const b = right as Record<string, unknown>;
  const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
  return [...keys].every((key) => sameValue(a[key], b[key]));
}

function sameMessage(left: ChatMessage, right: ChatMessage): boolean {
  return sameValue(left, right);
}

// Every frame is a full snapshot. Keep the previous frame's object for each
// unchanged message so finished messages are not re-rendered per frame.
function reuseMessages(previous: ChatState | undefined, next: ChatState): ChatState {
  if (!previous) return next;
  const messages = next.messages.map((message, index) => {
    const prior = previous.messages[index];
    return prior && sameMessage(prior, message) ? prior : message;
  });
  return { ...next, messages };
}

export function makeChatSession(options: {
  path: string;
  initialStatus: string;
  onState: (state: ChatState) => void;
  onError: (message: string) => void;
  onConnection: (connected: boolean) => void;
}) {
  let previous: ChatState | undefined;
  const connection = makeTerminalSessionController({
    initialStatus: options.initialStatus,
    url: () => {
      const url = new URL(options.path, location.href);
      url.searchParams.set("protocol", "acp");
      url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
      return url.href;
    },
    onOpen: () => options.onConnection(true),
    onDisconnected: () => options.onConnection(false),
    onMessage: (data) => {
      if (typeof data !== "string") return "continue";
      const decoded = decodeFrame(data);
      if (Option.isNone(decoded)) {
        options.onError("The host sent an invalid chat response.");
        return "stop";
      }
      if ("commandError" in decoded.value) options.onError(decoded.value.commandError);
      else {
        previous = reuseMessages(previous, decoded.value);
        options.onState(previous);
        if (!previous.connected) return "stop";
      }
      return "continue";
    },
  });
  return {
    program: connection.program,
    send(command: ChatCommand): boolean {
      if (!connection.isConnected()) return false;
      connection.send(JSON.stringify(command));
      return true;
    },
  };
}
