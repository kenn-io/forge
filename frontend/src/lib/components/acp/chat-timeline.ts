// Copied from the shared console chat.
import type { ChatMessage } from "./chat-types.js";

type ChatRow =
  | { kind: "message"; id: number; key: string; message: ChatMessage }
  | { kind: "tools"; id: number; key: string; messages: ChatMessage[] };

// Indices locate rows for streaming; protocol identities keep components alive
// when steering inserts messages before them.
export function chatRows(messages: readonly ChatMessage[], offset = 0): ChatRow[] {
  const rows: ChatRow[] = [];
  messages.forEach((message, position) => {
    const index = offset + position;
    if (message.role !== "tool" && message.role !== "thought") {
      rows.push({ kind: "message", id: index, key: message.id, message });
      return;
    }
    if (message.toolCallId?.startsWith("guardian_assessment:")) return;
    const last = rows.at(-1);
    if (last?.kind === "tools") {
      last.messages.push(message);
    } else rows.push({ kind: "tools", id: index, key: message.id, messages: [message] });
  });
  return rows;
}
