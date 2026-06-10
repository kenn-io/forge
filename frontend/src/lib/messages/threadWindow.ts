import type { MessageSummary } from "../api/messages/types";

export interface ThreadWindow {
  messages: MessageSummary[];
  truncated: boolean;
}

export function selectInclusiveWindow(sorted: MessageSummary[], selectedId: number, cap = 50): ThreadWindow {
  if (sorted.length <= cap) return { messages: sorted, truncated: false };

  const recent = sorted.slice(-cap);
  if (recent.some((message) => message.id === selectedId)) {
    return { messages: recent, truncated: true };
  }

  const selected = sorted.find((message) => message.id === selectedId);
  if (!selected) return { messages: recent, truncated: true };

  const messages = [...recent.slice(1), selected].sort((a, b) => a.sent_at.localeCompare(b.sent_at));
  return { messages, truncated: true };
}
