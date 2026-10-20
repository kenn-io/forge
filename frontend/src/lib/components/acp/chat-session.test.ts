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
function open(initial: ChatState, initialStatus = "running") {
  const states: ChatState[] = [];
  const errors = vi.fn();
  socket.sent = [];
  const session = makeChatSession({
    path: "/ws/chat",
    initialStatus,
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
  it("attaches errored chats and reports recovery failure without publishing an exit", () => {
    const initial = state([message("saved", "")]);
    const { states, errors, send } = open(initial, "error");
    expect(socket.options!.initialStatus).toBe("running");
    expect(send({ commandError: "Owner needs recovery", command: "connect", id: "" })).toBe("stop");
    expect(errors).toHaveBeenLastCalledWith("Owner needs recovery");
    expect(states).toEqual([initial]);
  });

  it("identifies rejected fields without exposing the transcript", () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      const { send, errors } = open(state([]));
      expect(
        send({ ...state([]), messageCursor: undefined, messages: [{ role: "user", text: "private transcript" }] }),
      ).toBe("stop");
      expect(errors).toHaveBeenLastCalledWith(expect.stringContaining("MissingKey"));
      expect(errors.mock.lastCall?.[0]).toContain("messages");
      expect(JSON.stringify(log.mock.calls)).not.toContain("private transcript");
      expect(errors.mock.lastCall?.[0]).not.toContain("private transcript");
    } finally {
      log.mockRestore();
    }
  });

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

  it("updates visible tool output before the newly appended tail arrives", () => {
    const first = message("first", "", { role: "user" });
    const tool = message("tool", "first", { role: "tool", rawOutput: "Before", version: 2 });
    const initial = state([first, tool], { messageCursor: 2 });
    const { states, send } = open(initial);
    // Updating this oversized tool then appending the tail produces two batches.
    const changed = { ...tool, version: 3, rawOutput: "After".repeat(60_000) };
    const tail = message("tail", "tool", { version: 4 });
    const metadata = { ...initial, messageCount: 3, lastMessageId: "tail" };
    send({ updates: { ...metadata, messages: [changed], messageCursor: 3, more: true } });
    expect(states.at(-1)!.messages).toEqual([first, changed]);
    send({ updates: { ...metadata, messages: [tail], messageCursor: 4, more: false } });
    expect(states.at(-1)!.messages).toEqual([first, changed, tail]);
  });

  it("shows connected new entries while each batch advertises a later undelivered tail", () => {
    const first = message("first", "", { role: "user" });
    const initial = state([first]);
    const { states, send, commands } = open(initial);
    const second = message("second", "first", { version: 2, text: "Second".repeat(50_000) });
    const third = message("third", "second", { version: 3, text: "Third".repeat(60_000) });
    const fourth = message("fourth", "third", { version: 4, text: "Fourth".repeat(50_000) });
    // Each oversized entry occupies a batch. Another entry is appended before
    // each read, so delivery never catches the latest global tail in this case.
    send({
      updates: {
        ...initial,
        messages: [second],
        messageCursor: 2,
        messageCount: 3,
        lastMessageId: "third",
        more: true,
      },
    });
    expect(states.at(-1)!.messages).toEqual([first, second]);
    send({
      updates: {
        ...initial,
        messages: [third],
        messageCursor: 3,
        messageCount: 4,
        lastMessageId: "fourth",
        more: true,
      },
    });
    expect(states.at(-1)!.messages).toEqual([first, second, third]);
    send({
      updates: {
        ...initial,
        messages: [fourth],
        messageCursor: 4,
        messageCount: 5,
        lastMessageId: "fifth",
        more: true,
      },
    });
    expect(states.at(-1)!.messages).toEqual([first, second, third, fourth]);
    expect(states.at(-1)!.messageOffset).toBe(0);
    expect(commands()).toEqual([]);
  });

  it("waits for a coalesced predecessor before showing its new successor", () => {
    const first = message("first", "", { role: "user" });
    const initial = state([first]);
    const { states, send } = open(initial);
    // Append tool v2, append reply v3, update tool v4, then append tail v5.
    // Coalescing moves the tool behind its reply in publication order.
    const reply = message("reply", "tool", { version: 3, text: "Reply".repeat(60_000) });
    const tool = message("tool", "first", { version: 4, role: "tool", rawOutput: "Result".repeat(50_000) });
    const metadata = { ...initial, lastMessageId: "tail", messageCount: 4, more: true };
    send({ updates: { ...metadata, messages: [reply], messageCursor: 3 } });
    expect(states.at(-1)!.messages).toEqual([first]);
    send({ updates: { ...metadata, messages: [tool], messageCursor: 4 } });
    expect(states.at(-1)!.messages).toEqual([first, tool, reply]);
  });

  it("shows a steering insertion in publication order while the latest tail is pending", () => {
    const first = message("first", "", { role: "user" });
    const tool = message("tool", "first", { version: 2, role: "tool", rawOutput: "Result".repeat(50_000) });
    const initial = state([first, tool], { messageCursor: 2 });
    const { states, send } = open(initial);
    const steer = message("steer", "first", { version: 3, role: "user", text: "Steer".repeat(60_000) });
    const metadata = { ...initial, lastMessageId: "tail", messageCount: 4, more: true };
    send({ updates: { ...metadata, messages: [steer], messageCursor: 3 } });
    expect(states.at(-1)!.messages).toEqual([first, steer, tool]);
    const relinked = { ...tool, previousId: "steer", version: 4 };
    send({ updates: { ...metadata, messages: [relinked], messageCursor: 4 } });
    expect(states.at(-1)!.messages).toEqual([first, steer, relinked]);
  });

  it("shows replies connected to retained output while a steering relink is delayed", () => {
    const first = message("first", "", { role: "user" });
    const tool = message("tool", "first", { version: 2, role: "tool" });
    const initial = state([first, tool], { messageCursor: 2 });
    const { states, send } = open(initial);
    // Insert steering v3 and relink tool v4; append reply v5, then update
    // tool v6 and append tail v7. Coalescing delays the relink behind reply.
    const steer = message("steer", "first", { version: 3, role: "user", text: "Steer".repeat(60_000) });
    const reply = message("reply", "tool", { version: 5, text: "Reply".repeat(60_000) });
    const metadata = { ...initial, lastMessageId: "tail", messageCount: 5, more: true };
    send({ updates: { ...metadata, messages: [steer], messageCursor: 3 } });
    expect(states.at(-1)!.messages).toEqual([first, steer, tool]);
    send({ updates: { ...metadata, messages: [reply], messageCursor: 5 } });
    expect(states.at(-1)!.messages).toEqual([first, steer, tool, reply]);
    const relinked = { ...tool, previousId: "steer", version: 6 };
    send({ updates: { ...metadata, messages: [relinked], messageCursor: 6 } });
    expect(states.at(-1)!.messages).toEqual([first, steer, relinked, reply]);
    const tail = message("tail", "reply", { version: 7 });
    send({ updates: { ...metadata, messages: [tail], messageCursor: 7, more: false } });
    expect(states.at(-1)!.messages).toEqual([first, steer, relinked, reply, tail]);
  });

  it("publishes requested history while a newer global tail is still pending", () => {
    const output = message("output", "tool", { version: 2 });
    const initial = state([output], { messageOffset: 1, messageCount: 2, messageCursor: 2 });
    const { session, states, send, commands } = open(initial);
    expect(session.loadEarlier()).toBe(true);
    expect(commands()).toEqual([{ type: "history", beforeId: "output", limit: 100 }]);
    const changed = { ...output, version: 3, text: "New output".repeat(30_000) };
    send({
      updates: {
        ...initial,
        messages: [changed],
        messageCursor: 3,
        messageCount: 3,
        lastMessageId: "tail",
        more: true,
      },
    });
    const tool = message("tool", "", { role: "tool" });
    send({ history: { beforeId: "output", offset: 0, messages: [tool] } });
    expect(states.at(-1)!.messages).toEqual([tool, changed]);
    expect(states.at(-1)!.messageOffset).toBe(0);
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
});
