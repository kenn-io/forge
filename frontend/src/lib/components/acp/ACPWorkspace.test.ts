import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/svelte";
import { Effect } from "effect";
import { tick } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import type { TerminalSessionOptions } from "../terminal/terminal-session.js";
import ACPWorkspace from "./ACPWorkspace.svelte";

// The websocket controller is the only mocked boundary: tests feed it raw
// state frames and read back the raw command frames the chat sends.
const socket = vi.hoisted(() => ({
  options: undefined as TerminalSessionOptions | undefined,
  sent: [] as string[],
}));
const runtimeCapture = vi.hoisted(() => ({ current: undefined as OwnedAppRuntime | undefined }));

vi.mock("../../app/runtime-context.js", () => ({ getAppRuntime: () => runtimeCapture.current }));
vi.mock("../terminal/terminal-session.js", async () => {
  const { Effect } = await import("effect");
  return {
    makeTerminalSessionController: (options: TerminalSessionOptions) => {
      socket.options = options;
      return {
        program: Effect.never,
        send: (data: string | Uint8Array) => {
          if (typeof data === "string") socket.sent.push(data);
        },
        isConnected: () => true,
      };
    },
  };
});

const baseState = {
  messages: [],
  configOptions: [],
  configuring: false,
  permissions: [],
  busy: false,
  connected: true,
  error: "",
};

async function openChat(state: Record<string, unknown>, props: { disabled?: boolean } = {}) {
  render(ACPWorkspace, { props: { websocketPath: "/ws/chat", ...props } });
  await waitFor(() => expect(socket.options).toBeDefined());
  socket.options!.onOpen?.();
  await push(state);
}

async function push(state: Record<string, unknown>) {
  // An unpaged fixture is the whole transcript.
  const messages = (state.messages ?? []) as unknown[];
  socket.options!.onMessage(
    JSON.stringify({ ...baseState, messageOffset: 0, messageCount: messages.length, ...state }),
  );
  await tick();
}

function sentCommands(): unknown[] {
  return socket.sent.map((frame) => JSON.parse(frame) as unknown);
}

beforeEach(() => {
  socket.options = undefined;
  socket.sent = [];
  runtimeCapture.current = makeAppRuntime();
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => ({ matches: false })),
  );
});

afterEach(async () => {
  cleanup();
  vi.unstubAllGlobals();
  if (runtimeCapture.current) await Effect.runPromise(runtimeCapture.current.disposeEffect);
  runtimeCapture.current = undefined;
});

describe("ACPWorkspace elicitations", () => {
  const deployForm = {
    id: "elicit-1",
    message: "Configure the deployment",
    schema: {
      properties: {
        name: { type: "string", title: "Project name", minLength: 2 },
        replicas: { type: "integer", title: "Replicas", minimum: 1, default: 2 },
        ratio: { type: "number", title: "Ratio" },
        verbose: { type: "boolean", title: "Verbose", default: true },
        tier: {
          type: "string",
          title: "Tier",
          oneOf: [
            { const: "free", title: "Free" },
            { const: "pro", title: "Pro" },
          ],
        },
        tags: { type: "array", title: "Tags", items: { type: "string", enum: ["alpha", "beta", "gamma"] } },
        contact: { type: "string", title: "Contact", format: "email" },
        extra: { type: "object", title: "Extra" },
      },
      required: ["name", "tier"],
    },
  };

  it("renders one labelled field per schema property with defaults", async () => {
    await openChat({ elicitations: [deployForm] });

    expect(screen.getByText("Configure the deployment")).toBeTruthy();
    expect((screen.getByLabelText(/Project name/) as HTMLInputElement).value).toBe("");
    expect((screen.getByLabelText(/Project name/) as HTMLInputElement).required).toBe(true);
    expect((screen.getByLabelText(/Replicas/) as HTMLInputElement).value).toBe("2");
    expect((screen.getByLabelText(/Ratio/) as HTMLInputElement).value).toBe("");
    expect((screen.getByRole("checkbox", { name: /Verbose/ }) as HTMLInputElement).checked).toBe(true);
    expect(screen.getByRole("combobox", { name: /Tier \(required\)/ })).toBeTruthy();
    const tags = screen.getByRole("group", { name: /Tags/ });
    expect(
      within(tags)
        .getAllByRole("checkbox")
        .map((box) => box.closest("label")?.textContent?.trim()),
    ).toEqual(["alpha", "beta", "gamma"]);
    expect(screen.getByLabelText(/Contact/).getAttribute("type")).toBe("email");
    expect(screen.getByLabelText(/Extra/).getAttribute("type")).toBe("text");
  });

  it("lists required fields first and keeps the remaining order", async () => {
    await openChat({
      elicitations: [
        {
          id: "elicit-order",
          message: "How should the migration run?",
          schema: {
            properties: {
              batchSize: { type: "integer", title: "Batch size" },
              dryRun: { type: "boolean", title: "Dry run first" },
              strategy: { type: "string", title: "Strategy", enum: ["backfill", "skip"] },
            },
            required: ["strategy"],
          },
        },
      ],
    });

    const form = screen.getByText("How should the migration run?").parentElement!;
    const text = form.textContent ?? "";
    const positions = ["Strategy", "Batch size", "Dry run first"].map((label) => text.indexOf(label));
    expect(positions.every((position) => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((a, b) => a - b));
  });

  it("submits the accept command with coerced content and omits empty optional fields", async () => {
    await openChat({ elicitations: [deployForm] });

    await fireEvent.input(screen.getByLabelText(/Project name/), { target: { value: "web" } });
    await fireEvent.input(screen.getByLabelText(/Replicas/), { target: { value: "3" } });
    await fireEvent.click(screen.getByRole("combobox", { name: /Tier/ }));
    await fireEvent.click(screen.getByRole("option", { name: "Pro" }));
    await fireEvent.click(within(screen.getByRole("group", { name: /Tags/ })).getByRole("checkbox", { name: "gamma" }));
    await fireEvent.click(within(screen.getByRole("group", { name: /Tags/ })).getByRole("checkbox", { name: "alpha" }));
    await fireEvent.input(screen.getByLabelText(/Extra/), { target: { value: "anything" } });
    await fireEvent.click(screen.getByRole("button", { name: "Submit" }));

    expect(sentCommands()).toEqual([
      {
        type: "elicitation",
        id: "elicit-1",
        action: "accept",
        content: { name: "web", replicas: 3, verbose: true, tier: "pro", tags: ["alpha", "gamma"], extra: "anything" },
      },
    ]);
  });

  it("keeps Submit disabled until required fields are filled and values are valid", async () => {
    await openChat({ elicitations: [deployForm] });
    const submit = screen.getByRole("button", { name: "Submit" }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);

    await fireEvent.input(screen.getByLabelText(/Project name/), { target: { value: "web" } });
    expect(submit.disabled).toBe(true);
    await fireEvent.click(screen.getByRole("combobox", { name: /Tier/ }));
    await fireEvent.click(screen.getByRole("option", { name: "Free" }));
    expect(submit.disabled).toBe(false);

    await fireEvent.input(screen.getByLabelText(/Replicas/), { target: { value: "2.5" } });
    expect(submit.disabled).toBe(true);
    expect(screen.getByText("Enter a whole number.")).toBeTruthy();
    await fireEvent.input(screen.getByLabelText(/Replicas/), { target: { value: "0" } });
    expect(submit.disabled).toBe(true);
    await fireEvent.input(screen.getByLabelText(/Replicas/), { target: { value: "4" } });
    expect(submit.disabled).toBe(false);

    await fireEvent.input(screen.getByLabelText(/Contact/), { target: { value: "not-an-email" } });
    expect(submit.disabled).toBe(true);
    await fireEvent.input(screen.getByLabelText(/Contact/), { target: { value: "dev@example.com" } });
    expect(submit.disabled).toBe(false);

    await fireEvent.input(screen.getByLabelText(/Project name/), { target: { value: "w" } });
    expect(submit.disabled).toBe(true);
    await fireEvent.click(submit);
    expect(sentCommands()).toEqual([]);
  });

  it("sends decline and cancel without content", async () => {
    await openChat({ elicitations: [deployForm] });

    await fireEvent.click(screen.getByRole("button", { name: "Decline" }));
    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(sentCommands()).toEqual([
      { type: "elicitation", id: "elicit-1", action: "decline" },
      { type: "elicitation", id: "elicit-1", action: "cancel" },
    ]);
  });

  it("disables every elicitation control while the chat is disabled", async () => {
    await openChat({ elicitations: [deployForm] }, { disabled: true });

    for (const name of ["Submit", "Decline", "Cancel"]) {
      expect((screen.getByRole("button", { name }) as HTMLButtonElement).disabled).toBe(true);
    }
    expect((screen.getByLabelText(/Project name/) as HTMLInputElement).disabled).toBe(true);
  });

  it("treats a missing or null elicitations field as no pending requests", async () => {
    await openChat({});
    expect(screen.queryByRole("button", { name: "Submit" })).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();

    await push({ elicitations: null, commands: null });
    expect(screen.queryByRole("button", { name: "Submit" })).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

describe("ACPWorkspace slash commands", () => {
  const commands = [
    { name: "review", description: "Review the current changes", inputHint: "pull request" },
    { name: "compact", description: "Summarize the conversation" },
    { name: "release-notes", description: "Draft release notes" },
    { name: "prereq", description: "Check prerequisites" },
  ];

  function composer(): HTMLTextAreaElement {
    return screen.getByRole("textbox", { name: "Message agent" }) as HTMLTextAreaElement;
  }

  function optionNames(): string[] {
    return within(screen.getByRole("listbox", { name: "Slash commands" }))
      .getAllByRole("option")
      .map((option) => option.querySelector(".command__name")?.textContent ?? "");
  }

  it("lists prefix matches before substring matches for the typed token", async () => {
    await openChat({ commands });

    await fireEvent.input(composer(), { target: { value: "/RE" } });
    expect(optionNames()).toEqual(["/review", "/release-notes", "/prereq"]);
    expect(screen.getByText("Review the current changes")).toBeTruthy();

    await fireEvent.input(composer(), { target: { value: "/review now" } });
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("moves the highlight with arrow keys and inserts the command on Enter instead of sending", async () => {
    await openChat({ commands });
    await fireEvent.input(composer(), { target: { value: "/re" } });
    expect(composer().getAttribute("aria-activedescendant")).toBe(screen.getAllByRole("option")[0]!.id);

    await fireEvent.keyDown(composer(), { key: "ArrowDown" });
    expect(screen.getAllByRole("option")[1]!.getAttribute("aria-selected")).toBe("true");
    expect(composer().getAttribute("aria-activedescendant")).toBe(screen.getAllByRole("option")[1]!.id);
    await fireEvent.keyDown(composer(), { key: "ArrowUp" });
    await fireEvent.keyDown(composer(), { key: "ArrowUp" });
    expect(screen.getAllByRole("option")[2]!.getAttribute("aria-selected")).toBe("true");
    await fireEvent.keyDown(composer(), { key: "ArrowDown" });
    await fireEvent.keyDown(composer(), { key: "ArrowDown" });
    await fireEvent.keyDown(composer(), { key: "Enter" });
    await tick();

    expect(composer().value).toBe("/release-notes ");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(sentCommands()).toEqual([]);
  });

  it("shows the inserted command's input hint until arguments are typed", async () => {
    await openChat({ commands });
    await fireEvent.input(composer(), { target: { value: "/rev" } });
    await fireEvent.click(screen.getByRole("option", { name: /\/review/ }));
    await tick();

    expect(composer().value).toBe("/review ");
    expect(composer().getAttribute("aria-describedby")).not.toBeNull();
    expect(document.getElementById(composer().getAttribute("aria-describedby")!)?.textContent).toContain(
      "pull request",
    );

    await fireEvent.input(composer(), { target: { value: "/review 42" } });
    expect(composer().getAttribute("aria-describedby")).toBeNull();
  });

  it("closes on Escape without changing the text, then Enter sends normally", async () => {
    await openChat({ commands });
    await fireEvent.input(composer(), { target: { value: "/re" } });
    expect(screen.getByRole("listbox")).toBeTruthy();

    await fireEvent.keyDown(composer(), { key: "Escape" });
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(composer().value).toBe("/re");

    await fireEvent.keyDown(composer(), { key: "Enter" });
    expect(sentCommands()).toEqual([{ type: "prompt", mode: "send", id: expect.any(String), text: "/re" }]);
  });

  it("sends on Enter when no command popup is open", async () => {
    await openChat({ commands });
    await fireEvent.input(composer(), { target: { value: "/zzz" } });
    expect(screen.queryByRole("listbox")).toBeNull();

    await fireEvent.keyDown(composer(), { key: "Enter" });
    expect(sentCommands()).toEqual([{ type: "prompt", mode: "send", id: expect.any(String), text: "/zzz" }]);
  });

  it("shows no popup when the agent advertises no commands", async () => {
    await openChat({ commands: [] });
    await fireEvent.input(composer(), { target: { value: "/" } });
    expect(screen.queryByRole("listbox")).toBeNull();

    await fireEvent.keyDown(composer(), { key: "Enter" });
    expect(sentCommands()).toEqual([{ type: "prompt", mode: "send", id: expect.any(String), text: "/" }]);
  });
});

describe("ACPWorkspace busy composer", () => {
  function composer(): HTMLTextAreaElement {
    return screen.getByRole("textbox", { name: "Message agent" }) as HTMLTextAreaElement;
  }

  it("stays enabled while busy and steers on Enter when the agent supports steering", async () => {
    await openChat({ busy: true, steeringSupported: true });

    expect(composer().disabled).toBe(false);
    expect(composer().placeholder).toBe("Steer the reply, or queue a follow-up…");
    // Queueing stays one chip away; Enter steers.
    expect(screen.getByRole("button", { name: "Queue" }).getAttribute("aria-keyshortcuts")).toBe("Alt+Enter");
    expect((screen.getByRole("button", { name: "Stop reply" }) as HTMLButtonElement).disabled).toBe(false);

    await fireEvent.input(composer(), { target: { value: "Focus on the tests" } });
    expect((screen.getByRole("button", { name: "Steer reply" }) as HTMLButtonElement).disabled).toBe(false);
    await fireEvent.keyDown(composer(), { key: "Enter" });

    expect(sentCommands()).toEqual([
      { type: "prompt", mode: "steer", id: expect.any(String), text: "Focus on the tests" },
    ]);
  });

  it("queues on Enter while busy when the agent cannot steer", async () => {
    await openChat({ busy: true, steeringSupported: false });

    expect(composer().placeholder).toBe("Queue a follow-up…");
    // The primary action already queues, so there is no second Queue control.
    expect(screen.queryByRole("button", { name: "Queue" })).toBeNull();
    await fireEvent.input(composer(), { target: { value: "Then update the docs" } });
    expect((screen.getByRole("button", { name: "Queue message" }) as HTMLButtonElement).disabled).toBe(false);
    await fireEvent.keyDown(composer(), { key: "Enter" });

    expect(sentCommands()).toEqual([
      { type: "prompt", mode: "queue", id: expect.any(String), text: "Then update the docs" },
    ]);
  });

  it("queues with Alt+Enter or the Queue button while steering is available", async () => {
    await openChat({ busy: true, steeringSupported: true });

    await fireEvent.input(composer(), { target: { value: "First follow-up" } });
    await fireEvent.keyDown(composer(), { key: "Enter", altKey: true });
    const [first] = sentCommands() as Array<{ id: string }>;
    expect(first).toEqual({ type: "prompt", mode: "queue", id: expect.any(String), text: "First follow-up" });
    expect(composer().value).toBe("First follow-up");

    await push({ busy: true, steeringSupported: true, queue: [{ id: first!.id, text: "First follow-up" }] });
    expect(composer().value).toBe("");

    await fireEvent.input(composer(), { target: { value: "Second follow-up" } });
    await fireEvent.click(screen.getByRole("button", { name: /^Queue/ }));
    expect(sentCommands()[1]).toEqual({
      type: "prompt",
      mode: "queue",
      id: expect.any(String),
      text: "Second follow-up",
    });
  });

  it("keeps the draft when the host rejects the prompt", async () => {
    await openChat({ busy: true });
    await fireEvent.input(composer(), { target: { value: "Keep this text" } });
    await fireEvent.keyDown(composer(), { key: "Enter" });

    const { id } = sentCommands()[0] as { id: string };
    socket.options!.onMessage(
      JSON.stringify({ commandError: "The agent is not accepting prompts.", command: "prompt", id }),
    );
    await tick();

    expect(screen.getByRole("alert").textContent).toContain("The agent is not accepting prompts.");
    expect(composer().value).toBe("Keep this text");
    expect((screen.getByRole("button", { name: "Queue message" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("keeps a prompt in flight when an unrelated command fails", async () => {
    await openChat({ busy: true });
    await fireEvent.input(composer(), { target: { value: "Keep this text" } });
    await fireEvent.keyDown(composer(), { key: "Enter" });

    for (const failed of [
      { command: "history", id: "" },
      { command: "config", id: "" },
      { command: "prompt", id: "another-submission" },
    ]) {
      socket.options!.onMessage(JSON.stringify({ commandError: "Something else failed.", ...failed }));
      await tick();
    }

    expect(screen.getByRole("alert").textContent).toContain("Something else failed.");
    // Still in flight: the prompt cannot be sent a second time.
    expect((screen.getByRole("button", { name: "Queue message" }) as HTMLButtonElement).disabled).toBe(true);
    expect(sentCommands().filter((command) => (command as { type: string }).type === "prompt")).toHaveLength(1);
  });

  it("lists queued prompts and removes one with unqueue", async () => {
    await openChat({
      busy: true,
      queue: [
        { id: "q1", text: "Run the full suite" },
        { id: "q2", text: "Summarize the diff" },
      ],
    });

    // The queue is one quiet chip until opened.
    const chip = screen.getByRole("button", { name: /2 queued/ });
    expect(chip.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("list", { name: "Queued messages" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Resume queue" })).toBeNull();

    await fireEvent.click(chip);
    const list = screen.getByRole("list", { name: "Queued messages" });
    expect(
      within(list)
        .getAllByRole("listitem")
        .map((item) => item.textContent?.trim()),
    ).toEqual(["Run the full suite", "Summarize the diff"]);
    expect(within(list).getByText("Run the full suite").getAttribute("title")).toBe("Run the full suite");

    await fireEvent.click(within(list).getAllByRole("button", { name: "Remove queued message" })[0]!);
    expect(sentCommands()).toEqual([{ type: "unqueue", id: "q1" }]);
  });

  it("shows a paused queue with Resume, and hides the section when the queue is empty", async () => {
    await openChat({ queuePaused: true, queue: [{ id: "q1", text: "Run the full suite" }] });

    expect(screen.getByRole("button", { name: /1 queued\s*·\s*paused/ })).toBeTruthy();
    await fireEvent.click(screen.getByRole("button", { name: "Resume queue" }));
    expect(sentCommands()).toEqual([{ type: "resume" }]);

    await push({ queue: [] });
    expect(screen.queryByRole("button", { name: /queued/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Resume queue" })).toBeNull();
  });
});

describe("ACPWorkspace sub-agents", () => {
  const tool = (fields: Record<string, unknown>) => ({ role: "tool", text: "", ...fields });
  const messages = [
    { role: "user", text: "Split the work" },
    tool({ text: "Explore repository", toolCallId: "s1", status: "in_progress", subagent: true }),
    tool({ text: "Read file", toolCallId: "c1", status: "completed", parentToolCallId: "s1" }),
    tool({ text: "Search code", toolCallId: "c2", status: "in_progress", parentToolCallId: "s1" }),
    tool({ text: "Write tests", toolCallId: "s2", status: "pending", subagent: true }),
    tool({ text: "Earlier helper", toolCallId: "s3", status: "completed", subagent: true }),
  ];

  it("shows running sub-agents with their child tool counts and hides once all finish", async () => {
    await openChat({ busy: true, messages });

    await fireEvent.click(screen.getByRole("button", { name: /2 sub-agents running/ }));
    const list = screen.getByRole("list", { name: "Running sub-agents" });
    expect(
      within(list)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual(["Explore repository 2 tool calls", "Write tests 0 tool calls"]);

    await push({
      messages: messages.map((message) => ("status" in message ? { ...message, status: "completed" } : message)),
    });
    expect(screen.queryByRole("button", { name: /sub-agents? running/ })).toBeNull();
    expect(screen.queryByRole("list", { name: "Running sub-agents" })).toBeNull();
  });

  it("marks sub-agent tool calls in the transcript", async () => {
    await openChat({ messages });

    const chip = screen.getByRole("button", { name: /5 tools · 3 sub-agents/ });
    await fireEvent.click(chip);
    const list = document.getElementById(chip.getAttribute("aria-controls")!)!;
    const explore = within(list)
      .getByText(/Explore repository/)
      .closest("li")!;
    expect(explore.textContent).toContain("Sub-agent · 2 tool calls");
    const readFile = within(list)
      .getByText(/Read file/)
      .closest("li")!;
    expect(readFile.textContent).not.toContain("Sub-agent");
  });
});

describe("ACPWorkspace chat status and transcript", () => {
  function statusText(): string {
    return document.querySelector(".chat-status")?.textContent?.trim() ?? "";
  }

  it("hides Guardian assessment tool calls from the transcript", async () => {
    await openChat({
      messages: [
        { role: "tool", text: "Guardian review", toolCallId: "guardian_assessment:1", status: "completed" },
        { role: "tool", text: "Read file", toolCallId: "t1", status: "completed" },
      ],
    });

    await fireEvent.click(screen.getByRole("button", { name: /^1 tool/ }));
    expect(screen.getByText("Read file")).toBeTruthy();
    expect(screen.queryByText("Guardian review")).toBeNull();
  });

  it("treats a tool call without a status as pending", async () => {
    await openChat({ messages: [{ role: "tool", text: "Run tests", toolCallId: "t1" }] });

    const chip = screen.getByRole("button", { name: /1 tool · running/ });
    await fireEvent.click(chip);
    expect(screen.getByText("Run tests").closest("li")?.textContent).toContain("pending");
  });

  it("reads Stopping and disables Stop until the turn ends", async () => {
    await openChat({ busy: true });
    expect(statusText()).toBe("Agent is replying…");
    expect((screen.getByRole("button", { name: "Stop reply" }) as HTMLButtonElement).disabled).toBe(false);

    await push({ busy: true, stopping: true });
    expect(statusText()).toBe("Stopping…");
    expect((screen.getByRole("button", { name: "Stop reply" }) as HTMLButtonElement).disabled).toBe(true);

    await push({});
    expect(statusText()).toBe("Agent");
    expect(screen.queryByRole("button", { name: "Stop reply" })).toBeNull();
  });

  it("reads Needs your answer while a permission or elicitation is pending", async () => {
    await openChat({
      busy: true,
      permissions: [
        { id: "p1", title: "Run a command", options: [{ optionId: "allow", name: "Allow", kind: "allow_once" }] },
      ],
    });
    expect(statusText()).toBe("Needs your answer");

    await push({
      busy: true,
      elicitations: [{ id: "e1", message: "Pick one", schema: { properties: {} } }],
    });
    expect(statusText()).toBe("Needs your answer");

    await push({ busy: true });
    expect(statusText()).toBe("Agent is replying…");
  });

  it("shows JSON-RPC error codes and data", async () => {
    await openChat({ error: "Internal error", errorCode: -32603, errorData: '{\n  "detail": "boom"\n}' });

    const alert = screen.getByRole("alert");
    expect(alert.querySelector("p")?.textContent).toBe("Internal error ACP -32603");
    expect(alert.querySelector("pre")?.textContent).toBe('{\n  "detail": "boom"\n}');

    await push({ error: "Agent exited" });
    expect(screen.getByRole("alert").textContent?.trim()).toBe("Agent exited");
    expect(screen.getByRole("alert").querySelector("pre")).toBeNull();
  });

  it("keeps the log quiet and announces only the finished reply", async () => {
    await openChat({
      busy: true,
      messages: [
        { role: "user", text: "Explain the change" },
        { role: "assistant", text: "The first paragraph" },
      ],
    });
    const log = screen.getByRole("log", { name: "Conversation" });
    expect(log.getAttribute("aria-live")).toBe("off");
    const announcer = document.querySelector('[aria-live="polite"][aria-atomic="true"]')!;
    expect(announcer.textContent).toBe("");

    await push({
      messages: [
        { role: "user", text: "Explain the change" },
        { role: "assistant", text: "The first paragraph" },
        { role: "assistant", text: "The final answer" },
      ],
    });
    expect(announcer.textContent).toBe("The final answer");
  });
});

describe("ACPWorkspace message gutter", () => {
  const createdAt = "2026-09-28T21:10:00Z";

  it("drops the per-message footer for a time and copy gutter", async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    await openChat({ messages: [{ role: "assistant", text: "The change is ready.", createdAt }] });

    const article = screen.getByRole("article", { name: "Assistant" });
    expect(within(article).queryByText("Agent")).toBeNull();
    const time = article.querySelector("time")!;
    expect(time.getAttribute("datetime")).toBe(createdAt);
    expect(time.textContent).toBe(new Date(createdAt).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }));

    await fireEvent.click(within(article).getByRole("button", { name: "Copy message" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("The change is ready."));
  });
});

describe("ACPWorkspace rich content", () => {
  const assistant = (content: Record<string, unknown>, text = "") => ({ role: "assistant", text, content });

  it("keeps a live thought open and folds it once the turn moves on", async () => {
    const thought = { role: "thought", text: "Weighing the two approaches" };
    await openChat({ busy: true, messages: [{ role: "user", text: "Pick one" }, thought] });

    const toggle = screen.getByRole("button", { name: "Thinking" });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(await screen.findByText("Weighing the two approaches")).toBeTruthy();

    await push({
      busy: true,
      messages: [{ role: "user", text: "Pick one" }, thought, { role: "assistant", text: "The first." }],
    });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("Weighing the two approaches")).toBeNull();

    await fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(await screen.findByText("Weighing the two approaches")).toBeTruthy();
  });

  it("starts a finished thought collapsed", async () => {
    await openChat({ messages: [{ role: "thought", text: "Done thinking" }] });
    expect(screen.getByRole("button", { name: "Thinking" }).getAttribute("aria-expanded")).toBe("false");
  });

  it("renders images from data and opens them in the image viewer", async () => {
    await openChat({
      messages: [assistant({ type: "image", mimeType: "image/png", data: "iVBORw0KGgo=", title: "Chart" })],
    });

    const image = screen.getByRole("img", { name: "Chart" }) as HTMLImageElement;
    expect(image.getAttribute("src")).toBe("data:image/png;base64,iVBORw0KGgo=");
    await fireEvent.click(screen.getByRole("button", { name: /Open image in expanded view: Chart/ }));
    // kit's MediaViewer loads on first open, so the dialog appears asynchronously.
    const viewer = await screen.findByRole("dialog", { name: "Chart" });
    expect(viewer.querySelector("img")?.getAttribute("src")).toBe("data:image/png;base64,iVBORw0KGgo=");
    await fireEvent.click(within(viewer).getByRole("button", { name: "Close expanded image" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("shows a placeholder for a payload that is not an image", async () => {
    await openChat({ messages: [assistant({ type: "image", mimeType: "text/html", data: "PGI+" })] });

    expect(screen.queryByRole("img")).toBeNull();
    expect(screen.getByText("Image unavailable")).toBeTruthy();
  });

  it("renders SVG images and SVG files inline", async () => {
    const svg = btoa('<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>');
    await openChat({
      messages: [
        assistant({ type: "image", mimeType: "image/svg+xml", data: svg }),
        assistant({ type: "resource", uri: "file:///work/logo.svg", mimeType: "image/svg+xml", data: svg }),
      ],
    });

    // An <img> renders SVG without running its scripts or loading resources.
    const sources = Array.from(document.querySelectorAll("img"), (image) => image.getAttribute("src"));
    expect(sources).toEqual([`data:image/svg+xml;base64,${svg}`, `data:image/svg+xml;base64,${svg}`]);
    expect(screen.queryByRole("button", { name: /Download/ })).toBeNull();
  });

  it("plays audio with native controls", async () => {
    await openChat({ messages: [assistant({ type: "audio", mimeType: "audio/wav", data: "UklGRg==" })] });

    const audio = document.querySelector("audio")!;
    expect(audio.getAttribute("src")).toBe("data:audio/wav;base64,UklGRg==");
    expect(audio.hasAttribute("controls")).toBe(true);
  });

  it("links resource cards only for web addresses", async () => {
    await openChat({
      messages: [
        assistant({
          type: "resource_link",
          uri: "https://example.com/spec",
          title: "Spec",
          description: "The design notes",
          size: 2048,
        }),
        assistant({ type: "resource_link", uri: "file:///repo/README.md", name: "README.md" }),
        assistant({ type: "resource_link", uri: "javascript:alert(1)", name: "Script" }),
      ],
    });

    const link = screen.getByRole("link", { name: "Spec" });
    expect(link.getAttribute("href")).toBe("https://example.com/spec");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    expect(screen.getByText("The design notes")).toBeTruthy();
    expect(screen.getByText("2.0 KB")).toBeTruthy();
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(screen.getByText("file:///repo/README.md").tagName).toBe("CODE");
    expect(screen.getByText("javascript:alert(1)").tagName).toBe("CODE");
  });

  it("shows embedded text resources as code without injecting markup", async () => {
    const html = "<b>bold</b><script>window.injected = true</script>";
    await openChat({
      messages: [assistant({ type: "resource", uri: "file:///repo/page.html", mimeType: "text/html", text: html })],
    });

    const article = screen.getByRole("article", { name: "Assistant" });
    expect(within(article).getByText("file:///repo/page.html", { selector: "summary span" })).toBeTruthy();
    expect(article.textContent).toContain(html);
    expect(article.querySelector("b, script")).toBeNull();
  });

  it("downloads binary resources through a blob URL named after the resource", async () => {
    const created: Blob[] = [];
    const createObjectURL = vi.fn((blob: Blob) => {
      created.push(blob);
      return "blob:report";
    });
    Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() });
    const clicked: HTMLAnchorElement[] = [];
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push(this);
    });
    try {
      await openChat({
        messages: [
          assistant({
            type: "resource",
            uri: "file:///repo/build/report.pdf?v=2",
            mimeType: "application/pdf",
            data: "JVBERi0=",
            size: 5,
          }),
        ],
      });
      await fireEvent.click(screen.getByRole("button", { name: "Download" }));

      expect(created[0]?.type).toBe("application/pdf");
      expect(await created[0]?.text()).toBe("%PDF-");
      expect(clicked[0]?.download).toBe("report.pdf");
      expect(clicked[0]?.getAttribute("href")).toBe("blob:report");
    } finally {
      click.mockRestore();
    }
  });

  it("expands tool rows into the diff, output, and raw JSON without repeating the path", async () => {
    await openChat({
      messages: [
        {
          role: "tool",
          text: "Edit app.ts",
          toolCallId: "t1",
          status: "completed",
          kind: "edit",
          locations: [{ path: "src/app.ts", line: 12 }],
          toolContent: [
            { type: "diff", path: "src/app.ts", oldText: "one\ntwo\nthree", newText: "one\nTWO\nthree" },
            { type: "content", content: { type: "text", text: "Applied 1 change" } },
            { type: "terminal", terminalId: "term-1" },
          ],
          rawInput: '{\n  "path": "src/app.ts"\n}',
          rawOutput: '{\n  "ok": true\n}',
        },
        { role: "tool", text: "Plain step", toolCallId: "t2", status: "completed" },
      ],
    });
    await fireEvent.click(screen.getByRole("button", { name: /^2 tools/ }));
    expect(screen.queryByRole("button", { name: /Plain step/ })).toBeNull();

    const row = screen.getByRole("button", { name: /Edit app\.ts/ });
    expect(row.getAttribute("aria-expanded")).toBe("false");
    await fireEvent.click(row);
    const details = document.getElementById(row.getAttribute("aria-controls")!)!;

    // The diff caption carries the location; the path is not listed twice.
    expect(within(details).queryByRole("list", { name: "Locations" })).toBeNull();
    expect(details.querySelector("figcaption")?.textContent).toBe("src/app.ts:12");
    const diff = within(details).getByRole("list", { name: "Changes to src/app.ts" });
    const kinds = ["context", "removed", "added"];
    expect(
      Array.from(diff.querySelectorAll("li"), (line) => [
        kinds.find((kind) => line.classList.contains(kind)),
        line.textContent,
      ]),
    ).toEqual([
      ["context", " one"],
      ["removed", "-Removed: two"],
      ["added", "+Added: TWO"],
      ["context", " three"],
    ]);
    expect(within(details).getByText("Applied 1 change").tagName).toBe("PRE");
    // A terminal with neither output nor an exit code adds no row.
    expect(within(details).queryByText(/Terminal|No output/)).toBeNull();
    expect(within(details).getByText("Raw input").closest("details")?.querySelector("pre")?.textContent).toBe(
      '{\n  "path": "src/app.ts"\n}',
    );
    expect(within(details).getByText("Raw output").closest("details")?.hasAttribute("open")).toBe(false);
  });

  it("summarizes the plan and hides it when empty", async () => {
    await openChat({
      plan: [
        { content: "Read the code", priority: "medium", status: "completed" },
        { content: "Write the fix", priority: "high", status: "completed" },
        { content: "Add tests", priority: "high", status: "in_progress" },
        { content: "Update docs", priority: "low", status: "pending" },
        { content: "Open a pull request", priority: "low", status: "pending" },
      ],
    });

    // The plan is one chip with its progress until opened.
    const chip = screen.getByRole("button", { name: /Plan 2\/5/ });
    expect(screen.queryByRole("list", { name: "Plan" })).toBeNull();
    await fireEvent.click(chip);
    const plan = screen.getByRole("list", { name: "Plan" });
    expect(within(plan).getAllByRole("listitem")).toHaveLength(5);
    expect(within(plan).getAllByRole("listitem")[2]!.textContent).toContain("In progress: Add tests");
    await fireEvent.click(chip);
    expect(screen.queryByRole("list", { name: "Plan" })).toBeNull();

    await push({ plan: [] });
    expect(screen.queryByRole("button", { name: /Plan/ })).toBeNull();
  });
});

describe("ACPWorkspace terminal output", () => {
  it("shows terminal output as plain text with the exit code", async () => {
    const esc = String.fromCharCode(27);
    await openChat({
      messages: [
        {
          role: "tool",
          text: "Run tests",
          toolCallId: "t1",
          status: "completed",
          kind: "execute",
          toolContent: [
            {
              type: "terminal",
              terminalId: "term-1",
              output: `${esc}[32mok${esc}[0m 12 passed\n${esc}]0;title${String.fromCharCode(7)}done`,
              exitCode: 0,
            },
            { type: "terminal", terminalId: "term-2", output: "FAIL one test", exitCode: 1 },
            { type: "terminal", terminalId: "term-3" },
          ],
        },
      ],
    });
    await fireEvent.click(screen.getByRole("button", { name: /^1 tool/ }));
    await fireEvent.click(screen.getByRole("button", { name: /Run tests/ }));

    const [passed, failed] = screen.getAllByLabelText("Command output");
    expect(screen.getAllByLabelText("Command output")).toHaveLength(2);
    expect(passed!.tagName).toBe("PRE");
    expect(passed!.textContent).toBe("ok 12 passed\ndone");
    const passedBadge = passed!.closest("figure")!.querySelector(".exit")!;
    expect(passedBadge.textContent).toBe("exit 0");
    expect(passedBadge.classList.contains("exit--ok")).toBe(true);

    const failedBadge = failed!.closest("figure")!.querySelector(".exit")!;
    expect(failedBadge.textContent).toBe("exit 1");
    expect(failedBadge.classList.contains("exit--failed")).toBe(true);

    // Terminal IDs are internal; a terminal with nothing to show adds no row.
    expect(screen.queryByText(/term-3/)).toBeNull();
  });
});

describe("ACPWorkspace transcript paging", () => {
  const user = (index: number) => ({ role: "user", text: `Message ${index}` });
  const range = (from: number, to: number) => Array.from({ length: to - from }, (_, offset) => user(from + offset));
  const texts = () => screen.queryAllByRole("article").map((article) => article.textContent?.trim());
  const historyRequests = () =>
    (sentCommands() as Array<{ type: string }>).filter((command) => command.type === "history");
  const conversation = () => document.querySelector<HTMLElement>(".conversation")!;

  it("renders a window at its offset and loads the page before it", async () => {
    await openChat({ messages: range(200, 202), messageOffset: 200, messageCount: 202 });
    expect(texts()).toEqual(["Message 200", "Message 201"]);

    await fireEvent.click(screen.getByRole("button", { name: "Load earlier messages" }));
    expect(historyRequests()).toEqual([{ type: "history", before: 200, limit: 100 }]);
    const loading = screen.getByRole("button", { name: "Loading earlier messages…" }) as HTMLButtonElement;
    expect(loading.disabled).toBe(true);

    // One request in flight: scrolling to the top does not ask again.
    await fireEvent.scroll(conversation());
    expect(historyRequests()).toHaveLength(1);

    socket.options!.onMessage(JSON.stringify({ history: { offset: 100, messages: range(100, 200) } }));
    await tick();
    expect(texts()).toHaveLength(102);
    expect(texts().slice(0, 2)).toEqual(["Message 100", "Message 101"]);
    expect(texts().slice(-3)).toEqual(["Message 199", "Message 200", "Message 201"]);

    await fireEvent.scroll(conversation());
    expect(historyRequests().at(-1)).toEqual({ type: "history", before: 100, limit: 100 });
  });

  it("asks for earlier history when the conversation scrolls near the top", async () => {
    await openChat({ messages: range(50, 52), messageOffset: 50, messageCount: 52 });
    await fireEvent.scroll(conversation());
    expect(historyRequests()).toEqual([{ type: "history", before: 50, limit: 100 }]);
  });

  it("keeps loaded messages when the window slides forward", async () => {
    await openChat({ messages: range(0, 3), messageOffset: 0, messageCount: 3 });
    await push({ messages: range(2, 5), messageOffset: 2, messageCount: 5 });

    expect(texts()).toEqual(["Message 0", "Message 1", "Message 2", "Message 3", "Message 4"]);
    expect(screen.queryByRole("button", { name: "Load earlier messages" })).toBeNull();
  });

  it("does not request history once the transcript start is loaded", async () => {
    await openChat({ messages: range(0, 2), messageOffset: 0, messageCount: 2 });
    expect(screen.queryByRole("button", { name: /earlier messages/ })).toBeNull();
    await fireEvent.scroll(conversation());
    expect(historyRequests()).toEqual([]);
  });

  it("re-enables loading after a failed request", async () => {
    await openChat({ messages: range(10, 12), messageOffset: 10, messageCount: 12 });
    await fireEvent.click(screen.getByRole("button", { name: "Load earlier messages" }));
    socket.options!.onMessage(JSON.stringify({ commandError: "History is unavailable.", command: "history", id: "" }));
    await tick();

    expect(screen.getByRole("alert").textContent).toContain("History is unavailable.");
    await fireEvent.click(screen.getByRole("button", { name: "Load earlier messages" }));
    expect(historyRequests()).toHaveLength(2);
  });

  it("uses transcript indices for the live tail of a paged window", async () => {
    await openChat({
      busy: true,
      messageOffset: 500,
      messageCount: 502,
      messages: [user(500), { role: "thought", text: "Planning the edit" }],
    });
    expect(screen.getByRole("button", { name: "Thinking" }).getAttribute("aria-expanded")).toBe("true");
  });

  it("starts over from the new window after reconnecting", async () => {
    await openChat({ messages: range(200, 202), messageOffset: 200, messageCount: 202 });
    await fireEvent.click(screen.getByRole("button", { name: "Load earlier messages" }));
    socket.options!.onMessage(JSON.stringify({ history: { offset: 100, messages: range(100, 200) } }));
    await tick();
    expect(texts()).toHaveLength(102);

    socket.options!.onOpen?.();
    await push({ messages: range(201, 203), messageOffset: 201, messageCount: 203 });
    expect(texts()).toEqual(["Message 201", "Message 202"]);
  });

  it("settles a resent prompt the host acknowledges outside the visible window", async () => {
    const composer = () => screen.getByRole("textbox", { name: "Message agent" }) as HTMLTextAreaElement;
    await openChat({ messages: range(200, 202), messageOffset: 200, messageCount: 202 });
    await fireEvent.input(composer(), { target: { value: "Sent while offline" } });
    await fireEvent.keyDown(composer(), { key: "Enter" });
    const prompts = () => sentCommands().filter((command) => (command as { type: string }).type === "prompt");
    const { id } = prompts()[0] as { id: string };

    // The host took the prompt while this chat was away, and it has since
    // left the window, so the resync resends it.
    socket.options!.onOpen?.();
    await push({ messages: range(400, 402), messageOffset: 400, messageCount: 402 });
    expect(prompts()).toHaveLength(2);
    expect((prompts()[1] as { id: string }).id).toBe(id);

    socket.options!.onMessage(JSON.stringify({ accepted: { command: "prompt", id } }));
    await tick();
    expect(composer().value).toBe("");
    await fireEvent.input(composer(), { target: { value: "Next" } });
    expect((screen.getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(false);
  });
});

describe("ACPWorkspace composer size", () => {
  it("sends drafts of any size", async () => {
    await openChat({});
    const text = "x".repeat(70_000);
    await fireEvent.input(screen.getByRole("textbox", { name: "Message agent" }), { target: { value: text } });

    expect(screen.queryByRole("alert")).toBeNull();
    const send = screen.getByRole("button", { name: "Send" }) as HTMLButtonElement;
    expect(send.disabled).toBe(false);
    await fireEvent.click(send);
    expect(sentCommands()).toEqual([{ type: "prompt", mode: "send", id: expect.any(String), text }]);
  });
});

describe("ACPWorkspace pasted images", () => {
  it("stages a removable image and sends an image-only prompt", async () => {
    await openChat(baseState);
    const input = screen.getByRole("textbox", { name: "Message agent" });
    const file = new File(["clipboard image"], "screenshot.png", { type: "image/png" });
    await fireEvent.paste(input, { clipboardData: { files: [file] } });
    await waitFor(() => expect(screen.getByRole("img", { name: "screenshot.png" })).toBeTruthy());
    await fireEvent.click(screen.getByRole("button", { name: "Remove screenshot.png" }));
    expect(screen.queryByRole("img", { name: "screenshot.png" })).toBeNull();
    await fireEvent.paste(input, { clipboardData: { files: [file] } });
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Send", exact: true }).hasAttribute("disabled")).toBe(false),
    );
    await fireEvent.click(screen.getByRole("button", { name: "Send", exact: true }));
    expect(sentCommands()).toContainEqual(
      expect.objectContaining({
        type: "prompt",
        text: "",
        images: [{ type: "image", mimeType: "image/png", data: btoa("clipboard image"), name: "screenshot.png" }],
      }),
    );
    socket.options!.onMessage(
      JSON.stringify({
        commandError: "Image rejected",
        command: "prompt",
        id: (sentCommands().at(-1) as { id: string }).id,
      }),
    );
    await tick();
    expect(screen.getByRole("img", { name: "screenshot.png" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Send", exact: true }).hasAttribute("disabled")).toBe(false);
  });

  it("leaves ordinary text paste to the textarea", async () => {
    await openChat(baseState);
    const event = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(event, "clipboardData", { value: { files: [] } });
    screen.getByRole("textbox", { name: "Message agent" }).dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
  });
});
