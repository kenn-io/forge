import { toolStatus, type ChatMessage } from "./chat-types.js";

export type RunningSubagent = { toolCallId: string; title: string; children: number };

function isRunning(message: ChatMessage): boolean {
  const status = toolStatus(message);
  return status === "pending" || status === "in_progress";
}

// Tool calls made inside each sub-agent, keyed by the sub-agent's toolCallId.
export function subagentChildCounts(messages: readonly ChatMessage[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const message of messages) {
    if (message.parentToolCallId) counts[message.parentToolCallId] = (counts[message.parentToolCallId] ?? 0) + 1;
  }
  return counts;
}

export function runningSubagents(
  messages: readonly ChatMessage[],
  counts: Readonly<Record<string, number>>,
): RunningSubagent[] {
  return messages.flatMap((message, index) => {
    if (message.role !== "tool" || !message.subagent || !isRunning(message)) return [];
    const toolCallId = message.toolCallId ?? `message-${index}`;
    return [
      { toolCallId, title: message.text || "Sub-agent", children: message.toolCallId ? (counts[toolCallId] ?? 0) : 0 },
    ];
  });
}
