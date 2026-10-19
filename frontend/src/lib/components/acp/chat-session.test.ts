import { describe, expect, it, vi } from "vite-plus/test";
import type { TerminalSessionOptions } from "../terminal/terminal-session.js";
import { makeChatSession } from "./chat-session.js";
import type { ChatState } from "./chat-types.js";

const socket = vi.hoisted(() => ({ options: undefined as TerminalSessionOptions | undefined, sent: [] as string[] }));
vi.mock("../terminal/terminal-session.js", async () => {
  const { Effect } = await import("effect");
  return {
    makeTerminalSessionController: (options: TerminalSessionOptions) => {
      socket.options = options;
      return { program: Effect.never, send: (data: string) => socket.sent.push(data), isConnected: () => true };
    },
  };
});

const frame = (messages: unknown[], fields: Record<string, unknown> = {}) =>
  JSON.stringify({
    messages,
    messageRevision: 0,
    historyRevision: 0,
    messageOffset: 0,
    messageCount: messages.length,
    configOptions: [],
    configuring: false,
    permissions: [],
    busy: true,
    connected: true,
    error: "",
    ...fields,
  });

describe("makeChatSession", () => {
  it.each(["earlier", "later"])("refreshes an older tool when %s output exceeds the snapshot budget", (largeTool) => {
    const states: ChatState[] = [];
    socket.sent = [];
    makeChatSession({
      path: "/ws/chat",
      initialStatus: "running",
      onState: (state) => states.push(state),
      onError: vi.fn(),
      onConnection: vi.fn(),
    });
    const earlier = { role: "tool", text: "Earlier", status: "pending" };
    const later = { role: "tool", text: "Later", status: "pending" };
    socket.options!.onMessage(frame([earlier, later], { messageRevision: 1 }));
    const oversized = largeTool === "later" ? { ...later, status: "completed", rawOutput: "x".repeat(300_000) } : later;
    socket.options!.onMessage(frame([oversized], { messageRevision: 2, messageOffset: 1, messageCount: 2 }));
    expect(socket.sent).toEqual([]);
    socket.options!.onMessage(
      frame([oversized], { messageRevision: 3, historyRevision: 3, messageOffset: 1, messageCount: 2 }),
    );
    expect(JSON.parse(socket.sent[0]!)).toEqual({ type: "history", before: 1, limit: 100 });
    const completed = {
      ...earlier,
      status: "completed",
      rawOutput: largeTool === "earlier" ? "x".repeat(300_000) : "done",
    };
    socket.options!.onMessage(JSON.stringify({ history: { revision: 3, offset: 0, messages: [completed] } }));
    expect(states.at(-1)!.messages).toEqual([completed, oversized]);
  });

  it("rebuilds shifted indices after steering and rejects a page from before the insertion", () => {
    const states: ChatState[] = [];
    socket.sent = [];
    const session = makeChatSession({
      path: "/ws/chat",
      initialStatus: "running",
      onState: (state) => states.push(state),
      onError: vi.fn(),
      onConnection: vi.fn(),
    });
    const first = { role: "user", text: "First" };
    const output = { role: "assistant", text: "x".repeat(300_000) };
    socket.options!.onMessage(frame([first, output], { messageRevision: 1, messageOffset: 1, messageCount: 3 }));
    session.loadEarlier();
    const steer = { role: "user", text: "Steer" };
    socket.options!.onMessage(
      frame([output], { messageRevision: 2, historyRevision: 2, messageOffset: 3, messageCount: 4 }),
    );
    socket.options!.onMessage(
      JSON.stringify({ history: { revision: 1, offset: 0, messages: [{ role: "user", text: "Old" }] } }),
    );
    expect(JSON.parse(socket.sent.at(-1)!)).toEqual({ type: "history", before: 3, limit: 100 });
    socket.options!.onMessage(JSON.stringify({ history: { revision: 2, offset: 2, messages: [steer] } }));
    expect(JSON.parse(socket.sent.at(-1)!)).toEqual({ type: "history", before: 2, limit: 100 });
    socket.options!.onMessage(JSON.stringify({ history: { revision: 2, offset: 1, messages: [first] } }));
    expect(states.at(-1)!.messages).toEqual([first, steer, output]);
    expect(states.at(-1)!.messageOffset).toBe(1);
  });

  it("waits for the matching snapshot when history overtakes an insertion update", () => {
    const states: ChatState[] = [];
    socket.sent = [];
    const session = makeChatSession({
      path: "/ws/chat",
      initialStatus: "running",
      onState: (state) => states.push(state),
      onError: vi.fn(),
      onConnection: vi.fn(),
    });
    const output = { role: "assistant", text: "Large output" };
    const first = { role: "user", text: "First" };
    const steer = { role: "user", text: "Steer" };
    socket.options!.onMessage(frame([output], { messageRevision: 1, messageOffset: 1, messageCount: 2 }));
    session.loadEarlier();
    socket.options!.onMessage(JSON.stringify({ history: { revision: 2, offset: 0, messages: [first] } }));
    expect(states.at(-1)!.messages).toEqual([output]);
    socket.options!.onMessage(
      frame([output], { messageRevision: 2, historyRevision: 2, messageOffset: 2, messageCount: 3 }),
    );
    socket.options!.onMessage(JSON.stringify({ history: { revision: 2, offset: 0, messages: [first, steer] } }));
    expect(states.at(-1)!.messages).toEqual([first, steer, output]);
  });

  it("finishes refreshing final tool results before stopping an exited agent's connection", () => {
    const states: ChatState[] = [];
    socket.sent = [];
    makeChatSession({
      path: "/ws/chat",
      initialStatus: "running",
      onState: (state) => states.push(state),
      onError: vi.fn(),
      onConnection: vi.fn(),
    });
    const tool = { role: "tool", text: "Tool", status: "pending" };
    const output = { role: "assistant", text: "Large output" };
    socket.options!.onMessage(frame([tool, output], { messageRevision: 1 }));
    expect(
      socket.options!.onMessage(
        frame([output], {
          messageRevision: 2,
          historyRevision: 2,
          messageOffset: 1,
          messageCount: 2,
          connected: false,
        }),
      ),
    ).toBe("continue");
    expect(JSON.parse(socket.sent.at(-1)!)).toEqual({ type: "history", before: 1, limit: 100 });
    const completed = { ...tool, status: "completed", rawOutput: "done" };
    expect(
      socket.options!.onMessage(JSON.stringify({ history: { revision: 2, offset: 0, messages: [completed] } })),
    ).toBe("stop");
    expect(states.at(-1)!.messages).toEqual([completed, output]);
  });

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
