import { Option, Schema } from "effect";
import { makeTerminalSessionController } from "../terminal/terminal-session.js";
import {
  ChatMessageSchema,
  ChatStateSchema,
  type ChatCommand,
  type ChatMessage,
  type ChatState,
} from "./chat-types.js";

const HistoryFrameSchema = Schema.Struct({
  history: Schema.Struct({ offset: Schema.Number, messages: Schema.Array(ChatMessageSchema) }),
});
const decodeFrame = Schema.decodeUnknownOption(
  Schema.fromJsonString(
    Schema.Union([
      ChatStateSchema,
      HistoryFrameSchema,
      // A failed command names itself so only that request is settled.
      Schema.Struct({ commandError: Schema.String, command: Schema.String, id: Schema.String }),
    ]),
  ),
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

// The loaded part of the transcript: messages[i] is transcript index start + i,
// always running contiguously to the end of the transcript. Snapshots carry
// only the latest window; older pages arrive as history replies. The host never
// drops messages, so pages that slide out of the window stay loaded.
type Transcript = { start: number; messages: ChatMessage[] };

// Writes a window at its absolute offset, keeping loaded messages below it and
// the previous object for every unchanged message, so finished messages are
// not re-rendered per frame.
function mergeWindow(loaded: Transcript, offset: number, window: readonly ChatMessage[], count: number): Transcript {
  const end = loaded.start + loaded.messages.length;
  // A window that does not touch what is loaded (first frame, or the view
  // fell behind) replaces it.
  if (loaded.messages.length === 0 || offset > end || offset < loaded.start) {
    return { start: offset, messages: [...window] };
  }
  const kept = loaded.messages.slice(0, offset - loaded.start);
  const merged = window.map((message, index) => {
    const prior = loaded.messages[offset - loaded.start + index];
    return prior && sameValue(prior, message) ? prior : message;
  });
  return { start: loaded.start, messages: [...kept, ...merged].slice(0, count - loaded.start) };
}

function prependPage(loaded: Transcript, offset: number, page: readonly ChatMessage[]): Transcript {
  // Only a page that reaches the loaded range extends it; a stale reply for a
  // transcript that has since been reset is dropped.
  if (offset >= loaded.start || offset + page.length < loaded.start) return loaded;
  return { start: offset, messages: [...page.slice(0, loaded.start - offset), ...loaded.messages] };
}

export function makeChatSession(options: {
  path: string;
  initialStatus: string;
  onState: (state: ChatState) => void;
  /** A failed command carries its type and ID; other errors carry neither. */
  onError: (message: string, failed?: { command: string; id: string }) => void;
  onConnection: (connected: boolean) => void;
  /** Whether an earlier-history request is in flight. */
  onHistoryLoading?: (loading: boolean) => void;
}) {
  let snapshot: ChatState | undefined;
  let transcript: Transcript = { start: 0, messages: [] };
  let historyLoading = false;
  const setHistoryLoading = (loading: boolean) => {
    if (historyLoading === loading) return;
    historyLoading = loading;
    options.onHistoryLoading?.(loading);
  };
  // Consumers see the whole loaded range; messageOffset is the transcript
  // index of messages[0].
  const publish = () => {
    if (!snapshot) return;
    options.onState({ ...snapshot, messages: transcript.messages, messageOffset: transcript.start });
  };
  const connection = makeTerminalSessionController({
    initialStatus: options.initialStatus,
    url: () => {
      const url = new URL(options.path, location.href);
      url.searchParams.set("protocol", "acp");
      url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
      return url.href;
    },
    // A new socket starts from its own window rather than trusting pages
    // loaded over the previous one.
    onOpen: () => {
      snapshot = undefined;
      transcript = { start: 0, messages: [] };
      options.onConnection(true);
    },
    onDisconnected: () => {
      setHistoryLoading(false);
      options.onConnection(false);
    },
    onMessage: (data) => {
      if (typeof data !== "string") return "continue";
      const decoded = decodeFrame(data);
      if (Option.isNone(decoded)) {
        options.onError("The host sent an invalid chat response.");
        return "stop";
      }
      const frame = decoded.value;
      if ("commandError" in frame) {
        if (frame.command === "history") setHistoryLoading(false);
        options.onError(frame.commandError, { command: frame.command, id: frame.id });
      } else if ("history" in frame) {
        transcript = prependPage(transcript, frame.history.offset, frame.history.messages);
        setHistoryLoading(false);
        publish();
      } else {
        transcript = mergeWindow(transcript, frame.messageOffset, frame.messages, frame.messageCount);
        snapshot = frame;
        publish();
        if (!frame.connected) return "stop";
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
    // Asks for the page before the earliest loaded message. One request is in
    // flight at a time, and none once the transcript start is loaded.
    loadEarlier(limit = 100): boolean {
      if (historyLoading || !snapshot || transcript.start === 0 || !connection.isConnected()) return false;
      connection.send(JSON.stringify({ type: "history", before: transcript.start, limit } satisfies ChatCommand));
      setHistoryLoading(true);
      return true;
    },
  };
}
