// Copied from the shared console chat.
import type { ChatMessage } from "./chat-types.js";

type ChatRow =
  | { kind: "message"; id: number; message: ChatMessage }
  | { kind: "tools"; id: number; messages: ChatMessage[] };

export function chatRows(messages: readonly ChatMessage[]): ChatRow[] {
  const rows: ChatRow[] = [];
  messages.forEach((message, index) => {
    if (message.role !== "tool") {
      rows.push({ kind: "message", id: index, message });
      return;
    }
    const last = rows.at(-1);
    if (last?.kind === "tools") last.messages.push(message);
    else rows.push({ kind: "tools", id: index, messages: [message] });
  });
  return rows;
}
