import { realpathSync } from "node:fs";
import { createInterface } from "node:readline";

const write = (message) => process.stdout.write(`${JSON.stringify({ jsonrpc: "2.0", ...message })}\n`);
const update = (value) => write({ method: "session/update", params: { sessionId: "workspace-chat", update: value } });
let prompt;
let configOptions = [
  {
    id: "model",
    name: "Model",
    category: "model",
    type: "select",
    currentValue: "fast",
    options: [
      {
        group: "models",
        name: "Available models",
        options: [
          { value: "fast", name: "Fast" },
          { value: "deep", name: "Deep" },
        ],
      },
    ],
  },
  {
    id: "effort",
    name: "Effort",
    category: "thought_level",
    type: "select",
    currentValue: "low",
    options: [
      { value: "low", name: "Low" },
      { value: "high", name: "High" },
    ],
  },
  {
    id: "mode",
    name: "Mode",
    category: "mode",
    type: "select",
    currentValue: "ask",
    options: [
      { value: "ask", name: "Ask" },
      { value: "plan", name: "Plan" },
    ],
  },
];
for await (const line of createInterface({ input: process.stdin })) {
  const request = JSON.parse(line);
  switch (request.method) {
    case "initialize":
      write({ id: request.id, result: { protocolVersion: 1, agentCapabilities: { mcpCapabilities: { http: true } } } });
      break;
    case "session/new":
      if (realpathSync(request.params.cwd) !== realpathSync(process.cwd()))
        throw new Error("Wrong workspace directory");
      for (const server of request.params.mcpServers) {
        if (server.name !== "kenn-forge") throw new Error("Unexpected MCP server");
        const response = await fetch(server.url, {
          method: "POST",
          headers: {
            ...Object.fromEntries(server.headers.map((header) => [header.name, header.value])),
            "Content-Type": "application/json",
            Accept: "application/json, text/event-stream",
          },
          body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list", params: {} }),
        });
        if (!response.ok || !(await response.text()).includes("kenn_forge_"))
          throw new Error("Forge MCP tools unavailable");
      }
      write({ id: request.id, result: { sessionId: "workspace-chat", configOptions } });
      break;
    case "session/set_config_option": {
      const option = configOptions.find((option) => option.id === request.params.configId);
      option.currentValue = request.params.value;
      if (option.id === "model") configOptions.find((option) => option.id === "effort").currentValue = "low";
      write({ id: request.id, result: { configOptions } });
      break;
    }
    case "session/prompt":
      prompt = request.id;
      if (request.params.prompt[0]?.text === "exit") {
        write({ id: prompt, result: { stopReason: "end_turn" } });
        setTimeout(() => process.exit(7), 10);
        break;
      }
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
