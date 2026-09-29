import { describe, expect, it, vi } from "vite-plus/test";
import type { TerminalSessionOptions } from "../terminal/terminal-session.js";
import { makeChatSession } from "./chat-session.js";
import type { ChatState } from "./chat-types.js";

const socket = vi.hoisted(() => ({ options: undefined as TerminalSessionOptions | undefined }));
vi.mock("../terminal/terminal-session.js", async () => {
  const { Effect } = await import("effect");
  return {
    makeTerminalSessionController: (options: TerminalSessionOptions) => {
      socket.options = options;
      return { program: Effect.never, send: () => undefined, isConnected: () => true };
    },
  };
});

const frame = (messages: unknown[]) =>
  JSON.stringify({
    messages,
    configOptions: [],
    configuring: false,
    historyTruncated: false,
    permissions: [],
    busy: true,
    connected: true,
    error: "",
  });

describe("makeChatSession", () => {
  it("keeps unchanged messages as the same objects across snapshot frames", () => {
    const states: ChatState[] = [];
    makeChatSession({
      path: "/ws/chat",
      initialStatus: "running",
      onState: (state) => states.push(state),
      onError: () => undefined,
      onConnection: () => undefined,
    });

    const question = { role: "user", text: "Explain the change", submissionId: "s1" };
    socket.options!.onMessage(frame([question, { role: "assistant", text: "First block." }]));
    socket.options!.onMessage(frame([question, { role: "assistant", text: "First block.\n\nSecond block." }]));
    socket.options!.onMessage(
      frame([
        question,
        { role: "assistant", text: "First block.\n\nSecond block." },
        { role: "tool", text: "Read file", status: "pending" },
      ]),
    );

    const [first, second, third] = states;
    expect(second!.messages[0]).toBe(first!.messages[0]);
    expect(second!.messages[1]).not.toBe(first!.messages[1]);
    expect(second!.messages[1]!.text).toBe("First block.\n\nSecond block.");
    expect(third!.messages[1]).toBe(second!.messages[1]);
    expect(third!.messages[2]!.status).toBe("pending");
  });
});

describe("makeChatSession nested content", () => {
  it("keeps messages with unchanged nested content as the same objects", () => {
    const states: ChatState[] = [];
    makeChatSession({
      path: "/ws/chat",
      initialStatus: "running",
      onState: (state) => states.push(state),
      onError: () => undefined,
      onConnection: () => undefined,
    });
    const tool = {
      role: "tool",
      text: "Edit app.ts",
      status: "completed",
      locations: [{ path: "src/app.ts", line: 3 }],
      toolContent: [{ type: "diff", path: "src/app.ts", oldText: "a", newText: "b" }],
    };
    const image = {
      role: "assistant",
      text: "",
      content: { type: "image", mimeType: "image/png", data: "iVBORw0KGgo=" },
    };
    socket.options!.onMessage(frame([tool, image]));
    socket.options!.onMessage(frame([tool, image, { role: "assistant", text: "Done." }]));
    socket.options!.onMessage(frame([{ ...tool, locations: [{ path: "src/app.ts", line: 4 }] }, image]));

    expect(states[1]!.messages[0]).toBe(states[0]!.messages[0]);
    expect(states[1]!.messages[1]).toBe(states[0]!.messages[1]);
    expect(states[2]!.messages[0]).not.toBe(states[1]!.messages[0]);
    expect(states[2]!.messages[1]).toBe(states[1]!.messages[1]);
  });
});
