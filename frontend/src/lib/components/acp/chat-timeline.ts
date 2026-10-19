// Copied from the shared console chat.
import type { ChatMessage } from "./chat-types.js";

type ChatRow =
  | { kind: "message"; id: number; key: string | number; message: ChatMessage }
  | { kind: "tools"; id: number; key: string | number; messages: ChatMessage[] };

// Indices locate rows for streaming; protocol identities keep components alive
// when steering inserts messages before them.
export function chatRows(messages: readonly ChatMessage[], offset = 0): ChatRow[] {
  const rows: ChatRow[] = [];
  const keys = messageKeys(messages, offset);
  messages.forEach((message, position) => {
    const index = offset + position;
    if (message.role !== "tool" && message.role !== "thought") {
      rows.push({ kind: "message", id: index, key: keys[position] ?? index, message });
      return;
    }
    if (message.toolCallId?.startsWith("guardian_assessment:")) return;
    const last = rows.at(-1);
    if (last?.kind === "tools") {
      if (message.toolCallId && !last.messages.some((entry) => entry.toolCallId)) {
        last.key = keys[position] ?? index;
      }
      last.messages.push(message);
    } else rows.push({ kind: "tools", id: index, key: keys[position] ?? index, messages: [message] });
  });
  return rows;
}

// Agents may reuse call IDs across turns. Only unique IDs can identify a row;
// ambiguous calls retain their transcript index instead of crashing rendering.
export function messageKeys(messages: readonly ChatMessage[], offset = 0): (string | number)[] {
  const counts = new Map<string, number>();
  for (const message of messages) {
    if (message.toolCallId) counts.set(message.toolCallId, (counts.get(message.toolCallId) ?? 0) + 1);
  }
  return messages.map((message, index) =>
    message.toolCallId && counts.get(message.toolCallId) === 1 ? `tool:${message.toolCallId}` : offset + index,
  );
}
