import { realpathSync } from "node:fs";
import { createInterface } from "node:readline";

const write = (message) => process.stdout.write(`${JSON.stringify({ jsonrpc: "2.0", ...message })}\n`);
const update = (value) => write({ method: "session/update", params: { sessionId: "workspace-chat", update: value } });
let prompt;
for await (const line of createInterface({ input: process.stdin })) {
  const request = JSON.parse(line);
  switch (request.method) {
    case "initialize":
      write({ id: request.id, result: { protocolVersion: 1, agentCapabilities: {} } });
      break;
    case "session/new":
      if (realpathSync(request.params.cwd) !== realpathSync(process.cwd()))
        throw new Error("Wrong workspace directory");
      write({ id: request.id, result: { sessionId: "workspace-chat" } });
      break;
    case "session/prompt":
      prompt = request.id;
      update({
        sessionUpdate: "agent_message_chunk",
        content: { type: "text", text: "I am working in the **workspace**.\n\n" },
      });
      update({
        sessionUpdate: "agent_message_chunk",
        content: { type: "text", text: "```ts\nconst ready = true;\n```\n" },
      });
      update({ sessionUpdate: "tool_call", toolCallId: "edit", title: "Update workspace file", status: "pending" });
      write({
        id: "permission",
        method: "session/request_permission",
        params: {
          sessionId: "workspace-chat",
          toolCall: { toolCallId: "edit", title: "Update workspace file" },
          options: [
            { optionId: "allow", name: "Allow once", kind: "allow_once" },
            { optionId: "deny", name: "Reject", kind: "reject_once" },
          ],
        },
      });
      break;
    case "session/cancel":
      write({ id: prompt, result: { stopReason: "cancelled" } });
      break;
    default:
      if (request.id === "permission") {
        update({ sessionUpdate: "tool_call_update", toolCallId: "edit", status: "completed" });
        update({
          sessionUpdate: "agent_message_chunk",
          content: { type: "text", text: "Permission received. The turn is complete." },
        });
        write({ id: prompt, result: { stopReason: "end_turn" } });
      }
  }
}
