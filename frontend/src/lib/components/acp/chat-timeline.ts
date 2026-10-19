// Copied from the shared console chat.
import type { ChatMessage } from "./chat-types.js";

export type ChatRow =
  | { kind: "message"; id: number; key: string; message: ChatMessage }
  | { kind: "tools"; id: number; key: string; messages: ChatMessage[] };

// Indices locate rows for streaming; protocol identities keep components alive
// when steering inserts messages before them.
export function chatRows(messages: readonly ChatMessage[], offset = 0, previous: readonly ChatRow[] = []): ChatRow[] {
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
  const priorGroups = new Set(previous.filter((row) => row.kind === "tools").map((row) => row.key));
  for (const row of rows) {
    if (row.kind === "tools") {
      // Keep the existing group mounted when a page prepends adjacent activity.
      // A split keeps that key only with the entry that originally owned it.
      row.key = row.messages.find((message) => priorGroups.has(message.id))?.id ?? row.key;
    }
  }
  return rows;
}
