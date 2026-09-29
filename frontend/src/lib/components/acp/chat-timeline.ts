// Copied from the shared console chat.
import type { ChatMessage } from "./chat-types.js";

type ChatRow =
  | { kind: "message"; id: number; message: ChatMessage }
  | { kind: "tools"; id: number; messages: ChatMessage[] };

// Row ids are transcript indices (offset is the index of messages[0]), so
// rows keep their identity when earlier pages are prepended.
export function chatRows(messages: readonly ChatMessage[], offset = 0): ChatRow[] {
  const rows: ChatRow[] = [];
  messages.forEach((message, position) => {
    const index = offset + position;
    if (message.role !== "tool") {
      rows.push({ kind: "message", id: index, message });
      return;
    }
    if (message.toolCallId?.startsWith("guardian_assessment:")) return;
    const last = rows.at(-1);
    if (last?.kind === "tools") last.messages.push(message);
    else rows.push({ kind: "tools", id: index, messages: [message] });
  });
  return rows;
}
