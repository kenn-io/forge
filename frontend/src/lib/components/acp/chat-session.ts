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
  const successors = new Map<string, string>();
  let displayed: ChatMessage[] = [];
  let displayedOffset = 0;
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
      if (prior && entry.version <= prior.version) continue;
      if (prior && successors.get(prior.previousId) === prior.id) successors.delete(prior.previousId);
      messages.set(entry.id, entry);
      const successor = messages.get(successors.get(entry.previousId) ?? "");
      // During insertion, an old successor link can coexist with its newer
      // replacement until the following batch supplies the relinked entry.
      if (!successor || entry.version > successor.version) successors.set(entry.previousId, entry.id);
    }
  };
  const publish = () => {
    if (!snapshot) return;
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
    // Publish complete chains as they arrive, even while more changes remain.
    // A coalesced predecessor or the advertised global tail can arrive later.
    // Keep visible output moving without requiring that latest tail.
    const complete = id === boundary || !id;
    if (complete) {
      displayed = ordered.reverse();
      displayedOffset = snapshot.messageCount - displayed.length;
    } else {
      const connected: ChatMessage[] = [];
      const visible = new Set<string>();
      const appendChain = (start: string | undefined) => {
        let next = start;
        while (next && !visible.has(next)) {
          const entry = messages.get(next);
          if (!entry) break;
          connected.push(entry);
          visible.add(entry.id);
          next = successors.get(entry.id);
        }
      };
      appendChain(successors.get(boundary));
      // Retain the established suffix across a missing relink, and keep its
      // received replies advancing too. Already visited entries are not repeated.
      for (const entry of displayed) appendChain(entry.id);
      displayed = connected;
    }
    // Retain updates to unloaded entries while a page is in flight so a late
    // older page cannot overwrite them. Otherwise only the visible range stays.
    if (complete && !batching && historyBefore === undefined) {
      for (const [key, entry] of messages) {
        if (seen.has(key)) continue;
        messages.delete(key);
        if (successors.get(entry.previousId) === key) successors.delete(entry.previousId);
      }
    }
    const displayedConnected = snapshot.connected || batching || historyBefore !== undefined;
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
      successors.clear();
      displayed = [];
      displayedOffset = 0;
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
          if (first) {
            boundary = messages.get(first.id)!.previousId;
            displayedOffset = frame.history.offset;
          }
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
        successors.clear();
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
