import { describe, expect, it, vi } from "vite-plus/test";
import type { TerminalSessionOptions } from "../terminal/terminal-session.js";
import { makeChatSession } from "./chat-session.js";
import type { ChatMessage, ChatState } from "./chat-types.js";

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

const message = (id: string, previousId: string, fields: Partial<ChatMessage> = {}): ChatMessage => ({
  id,
  previousId,
  version: 1,
  role: "assistant",
  text: id,
  ...fields,
});
const state = (messages: ChatMessage[], fields: Partial<ChatState> = {}): ChatState => ({
  messages,
  messageCursor: 1,
  lastMessageId: messages.at(-1)?.id ?? "",
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
function open(initial: ChatState) {
  const states: ChatState[] = [];
  const errors = vi.fn();
  socket.sent = [];
  const session = makeChatSession({
    path: "/ws/chat",
    initialStatus: "running",
    onState: (next) => states.push(next),
    onError: errors,
    onConnection: vi.fn(),
  });
  socket.options!.onOpen?.();
  const send = (frame: unknown) => socket.options!.onMessage(JSON.stringify(frame));
  send(initial);
  return { session, states, errors, send, commands: () => socket.sent.map((frame) => JSON.parse(frame)) };
}

describe("makeChatSession ID upserts", () => {
  it("updates an older loaded tool directly without fetching history", () => {
    const tool = message("tool", "", { role: "tool", status: "pending" });
    const output = message("output", "tool", { text: "x".repeat(300_000) });
    const initial = state([tool, output]);
    const { states, send, commands } = open(initial);
    const completed = { ...tool, version: 2, status: "completed", rawOutput: "Finished" };
    send({ updates: { ...initial, messages: [completed], messageCursor: 2, more: false } });
    expect(states.at(-1)!.messages).toEqual([completed, output]);
    expect(states.at(-1)!.messages[1]).toBe(states[0]!.messages[1]);
    expect(commands()).toEqual([]);
  });

  it("orders a steering insertion by links without duplicating output or fetching history", () => {
    const first = message("first", "", { role: "user" });
    const output = message("output", "first");
    const initial = state([first, output]);
    const { states, send, commands } = open(initial);
    const steer = message("steer", "first", { role: "user", version: 2 });
    const shifted = { ...output, previousId: "steer", version: 2 };
    send({ updates: { ...initial, messages: [shifted, steer], messageCount: 3, messageCursor: 2, more: false } });
    expect(states.at(-1)!.messages).toEqual([first, steer, shifted]);
    expect(states.at(-1)!.messageOffset).toBe(0);
    expect(commands()).toEqual([]);
  });

  it("keeps a newer unloaded tool update when an older history page arrives", () => {
    const output = message("output", "tool");
    const initial = state([output], { messageOffset: 2, messageCount: 3 });
    const { session, states, send, commands } = open(initial);
    expect(session.loadEarlier()).toBe(true);
    expect(commands()).toEqual([{ type: "history", beforeId: "output", limit: 100 }]);
    const completed = message("tool", "first", {
      role: "tool",
      version: 2,
      status: "completed",
      rawOutput: "Final result",
    });
    send({ updates: { ...initial, messages: [completed], messageCursor: 2, more: false } });
    expect(states.at(-1)!.messages).toEqual([output]);
    send({
      history: {
        beforeId: "output",
        offset: 0,
        messages: [message("first", ""), { ...completed, version: 1, status: "pending", rawOutput: "Stale" }],
      },
    });
    expect(states.at(-1)!.messages).toEqual([message("first", ""), completed, output]);
    expect(states.at(-1)!.messageOffset).toBe(0);
    expect(commands()).toHaveLength(1);
  });

  it("does not expand the visible range when an unloaded older entry changes", () => {
    const output = message("output", "tool");
    const initial = state([output], { messageOffset: 2, messageCount: 3 });
    const { states, send, commands } = open(initial);
    send({
      updates: { ...initial, messages: [message("tool", "first", { version: 2 })], messageCursor: 2, more: false },
    });
    expect(states.at(-1)!.messages).toEqual([output]);
    expect(states.at(-1)!.messageOffset).toBe(2);
    expect(commands()).toEqual([]);
  });

  it("publishes final results progressively and stops only after the last batch", () => {
    const first = message("first", "");
    const second = message("second", "first");
    const initial = state([first, second]);
    const { states, send, commands } = open(initial);
    const changedFirst = { ...first, version: 2, text: "x".repeat(300_000) };
    const changedSecond = { ...second, version: 2, text: "Final result" };
    expect(
      send({ updates: { ...initial, messages: [changedFirst], messageCursor: 1, connected: false, more: true } }),
    ).toBe("continue");
    expect(states.at(-1)!.messages).toEqual([changedFirst, second]);
    expect(states.at(-1)!.connected).toBe(true);
    expect(
      send({ updates: { ...initial, messages: [changedSecond], messageCursor: 2, connected: false, more: false } }),
    ).toBe("stop");
    expect(states.at(-1)!.messages).toEqual([changedFirst, changedSecond]);
    expect(states.at(-1)!.connected).toBe(false);
    expect(commands()).toEqual([]);
  });

  it("keeps displayed entries coherent when a history reply arrives between update batches", () => {
    const output = message("output", "tool");
    const initial = state([output], { messageOffset: 1, messageCount: 2 });
    const { session, states, send } = open(initial);
    session.loadEarlier();
    const tool = message("tool", "", { version: 2, text: "New result" });
    send({ updates: { ...initial, messages: [tool], more: true } });
    send({ history: { beforeId: "output", offset: 0, messages: [{ ...tool, version: 1, text: "Old result" }] } });
    expect(states.at(-1)!.messages).toEqual([tool, output]);
    send({ updates: { ...initial, messages: [], messageCursor: 2, more: false } });
    expect(states.at(-1)!.messages).toEqual([tool, output]);
  });

  it("advances tool results and new entries while updates keep arriving between batches", () => {
    const first = message("first", "", { role: "tool", rawOutput: "Initial first".repeat(15_000) });
    const second = message("second", "first", { role: "tool", rawOutput: "Initial second".repeat(15_000) });
    const initial = state([first, second]);
    const { states, send, commands } = open(initial);
    for (const version of [2, 3, 4]) {
      send({
        updates: {
          ...initial,
          messages: [{ ...first, version, rawOutput: `First ${version}`.repeat(30_000) }],
          more: true,
        },
      });
      expect(states.at(-1)!.messages[0]!.rawOutput).toBe(`First ${version}`.repeat(30_000));
      send({
        updates: {
          ...initial,
          messages: [{ ...second, version, rawOutput: `Second ${version}`.repeat(30_000) }],
          more: true,
        },
      });
      expect(states.at(-1)!.messages[1]!.rawOutput).toBe(`Second ${version}`.repeat(30_000));
    }
    const next = message("next", "second", { text: "More output" });
    send({ updates: { ...initial, messages: [next], lastMessageId: "next", messageCount: 3, more: true } });
    expect(states.at(-1)!.messages.map((entry) => entry.id)).toEqual(["first", "second", "next"]);
    expect(commands()).toEqual([]);
  });

  it("keeps visible output advancing while a steering predecessor waits in another batch", () => {
    const first = message("first", "", { role: "user" });
    const tool = message("tool", "first", { role: "tool", rawOutput: "Before" });
    const initial = state([first, tool]);
    const { states, send } = open(initial);
    const changed = { ...tool, previousId: "steer", version: 2, rawOutput: "After" };
    send({ updates: { ...initial, messages: [changed], messageCount: 3, more: true } });
    expect(states.at(-1)!.messages.map((entry) => entry.id)).toEqual(["first", "tool"]);
    expect(states.at(-1)!.messages[1]!.rawOutput).toBe("After");
    const steer = message("steer", "first", { role: "user" });
    send({ updates: { ...initial, messages: [steer], messageCount: 3, more: true } });
    expect(states.at(-1)!.messages).toEqual([first, steer, changed]);
  });

  it("settles a requested history page before closing after the final update", () => {
    const output = message("output", "tool");
    const initial = state([output], { messageOffset: 1, messageCount: 2 });
    const { session, states, send } = open(initial);
    session.loadEarlier();
    const tool = message("tool", "", { version: 2, text: "Final result" });
    expect(send({ updates: { ...initial, messages: [tool], messageCursor: 2, connected: false, more: false } })).toBe(
      "continue",
    );
    expect(states.at(-1)!.connected).toBe(true);
    expect(
      send({ history: { beforeId: "output", offset: 0, messages: [{ ...tool, version: 1, text: "Old result" }] } }),
    ).toBe("stop");
    expect(states.at(-1)!.messages).toEqual([tool, output]);
    expect(states.at(-1)!.connected).toBe(false);
  });

  it("retains unchanged nested-content objects by entry version", () => {
    const tool = message("tool", "", {
      role: "tool",
      locations: [{ path: "src/app.ts", line: 3 }],
      toolContent: [{ type: "diff", path: "src/app.ts", oldText: "a", newText: "b" }],
    });
    const output = message("output", "tool");
    const initial = state([tool, output]);
    const { states, send } = open(initial);
    send({
      updates: {
        ...initial,
        messages: [tool, { ...output, text: "Changed", version: 2 }],
        messageCursor: 2,
        more: false,
      },
    });
    expect(states[1]!.messages[0]).toBe(states[0]!.messages[0]);
    expect(states[1]!.messages[1]).not.toBe(states[0]!.messages[1]);
    expect(states[1]!.messages[1]!.text).toBe("Changed");
  });
});
