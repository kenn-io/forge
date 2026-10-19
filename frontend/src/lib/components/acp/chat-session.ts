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
  history: Schema.Struct({ beforeId: Schema.String, offset: Schema.Number, messages: Schema.Array(ChatMessageSchema) }),
});
const UpdateFrameSchema = Schema.Struct({
  updates: Schema.Struct({ ...ChatStateSchema.fields, more: Schema.Boolean }),
});
const decodeFrame = Schema.decodeUnknownOption(
  Schema.fromJsonString(
    Schema.Union([
      ChatStateSchema,
      UpdateFrameSchema,
      HistoryFrameSchema,
      Schema.Struct({ commandError: Schema.String, command: Schema.String, id: Schema.String }),
      Schema.Struct({ accepted: Schema.Struct({ command: Schema.String, id: Schema.String }) }),
    ]),
  ),
);

export function makeChatSession(options: {
  path: string;
  initialStatus: string;
  onState: (state: ChatState) => void;
  /** A failed command carries its type and ID; other errors carry neither. */
  onError: (message: string, failed?: { command: string; id: string }) => void;
  /** The host took a command this client sent. */
  onAccepted?: (accepted: { command: string; id: string }) => void;
  onConnection: (connected: boolean) => void;
  /** Whether an earlier-history request is in flight. */
  onHistoryLoading?: (loading: boolean) => void;
}) {
  let snapshot: ChatState | undefined;
  const messages = new Map<string, ChatMessage>();
  let displayed: ChatMessage[] = [];
  let displayedOffset = 0;
  let displayedConnected = false;
  // Do not load older entries merely because an agent updates them. This is
  // the predecessor of the range the reader has explicitly loaded.
  let boundary = "";
  let historyBefore: string | undefined;
  let batching = false;
  const setHistoryLoading = (beforeId: string | undefined) => {
    const wasLoading = historyBefore !== undefined;
    historyBefore = beforeId;
    if (wasLoading !== (beforeId !== undefined)) options.onHistoryLoading?.(beforeId !== undefined);
  };
  const upsert = (entries: readonly ChatMessage[]) => {
    for (const entry of entries) {
      const prior = messages.get(entry.id);
      if (!prior || entry.version > prior.version) messages.set(entry.id, entry);
    }
  };
  const publish = () => {
    if (!snapshot) return;
    if (batching) {
      options.onState({
        ...snapshot,
        messages: displayed,
        messageOffset: displayedOffset,
        connected: displayedConnected,
      });
      return;
    }
    const ordered: ChatMessage[] = [];
    const seen = new Set<string>();
    let id = snapshot.lastMessageId;
    while (id && id !== boundary && !seen.has(id)) {
      const entry = messages.get(id);
      if (!entry) break;
      seen.add(id);
      ordered.push(entry);
      id = entry.previousId;
    }
    displayed = ordered.reverse();
    // Retain updates to unloaded entries while a page is in flight so a late
    // older page cannot overwrite them. Otherwise only the visible range stays.
    if (historyBefore === undefined) {
      for (const key of messages.keys()) if (!seen.has(key)) messages.delete(key);
    }
    displayedOffset = snapshot.messageCount - displayed.length;
    displayedConnected = snapshot.connected || historyBefore !== undefined;
    options.onState({
      ...snapshot,
      messages: displayed,
      messageOffset: displayedOffset,
      connected: displayedConnected,
    });
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
      messages.clear();
      displayed = [];
      displayedOffset = 0;
      displayedConnected = false;
      boundary = "";
      batching = false;
      setHistoryLoading(undefined);
      options.onConnection(true);
    },
    onDisconnected: () => {
      setHistoryLoading(undefined);
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
        if (frame.command === "history") {
          setHistoryLoading(undefined);
          publish();
        }
        options.onError(frame.commandError, { command: frame.command, id: frame.id });
      } else if ("accepted" in frame) {
        options.onAccepted?.(frame.accepted);
      } else if ("history" in frame) {
        if (frame.history.beforeId === historyBefore) {
          upsert(frame.history.messages);
          const first = frame.history.messages[0];
          if (first) boundary = messages.get(first.id)!.previousId;
          setHistoryLoading(undefined);
          publish();
        }
      } else if ("updates" in frame) {
        upsert(frame.updates.messages);
        snapshot = frame.updates;
        batching = frame.updates.more;
        publish();
      } else {
        messages.clear();
        upsert(frame.messages);
        boundary = frame.messages[0]?.previousId ?? "";
        snapshot = frame;
        batching = false;
        publish();
      }
      if (snapshot && !snapshot.connected && !batching && historyBefore === undefined) return "stop";
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
    // History is loaded only on the reader's request, never to recover an update.
    loadEarlier(limit = 100): boolean {
      const first = displayed[0];
      if (historyBefore !== undefined || !first?.previousId || !connection.isConnected()) return false;
      setHistoryLoading(first.id);
      connection.send(JSON.stringify({ type: "history", beforeId: first.id, limit } satisfies ChatCommand));
      return true;
    },
  };
}
