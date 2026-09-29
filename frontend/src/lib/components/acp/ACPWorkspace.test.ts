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
  historyTruncated: false,
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
  socket.options!.onMessage(JSON.stringify({ ...baseState, ...state }));
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
    expect(screen.getByText("pull request")).toBeTruthy();

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
    expect(screen.getByText("Enter steers this reply")).toBeTruthy();
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
    expect(screen.queryByText("Enter steers this reply")).toBeNull();
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

    socket.options!.onMessage(JSON.stringify({ commandError: "The agent is not accepting prompts." }));
    await tick();

    expect(screen.getByRole("alert").textContent).toContain("The agent is not accepting prompts.");
    expect(composer().value).toBe("Keep this text");
    expect((screen.getByRole("button", { name: "Queue message" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("lists queued prompts and removes one with unqueue", async () => {
    await openChat({
      busy: true,
      queue: [
        { id: "q1", text: "Run the full suite" },
        { id: "q2", text: "Summarize the diff" },
      ],
    });

    const section = screen.getByRole("group", { name: "Queued messages" });
    expect(within(section).getByText("2 queued · sends after this reply")).toBeTruthy();
    expect(
      within(section)
        .getAllByRole("listitem")
        .map((item) => item.textContent?.trim()),
    ).toEqual(["Run the full suite", "Summarize the diff"]);
    expect(within(section).getByText("Run the full suite").getAttribute("title")).toBe("Run the full suite");
    expect(within(section).queryByRole("button", { name: "Resume queue" })).toBeNull();

    await fireEvent.click(within(section).getAllByRole("button", { name: "Remove queued message" })[0]!);
    expect(sentCommands()).toEqual([{ type: "unqueue", id: "q1" }]);
  });

  it("shows a paused queue with Resume, and hides the section when the queue is empty", async () => {
    await openChat({ queuePaused: true, queue: [{ id: "q1", text: "Run the full suite" }] });

    const section = screen.getByRole("group", { name: "Queued messages" });
    expect(within(section).getByText("1 queued · Paused")).toBeTruthy();
    await fireEvent.click(within(section).getByRole("button", { name: "Resume queue" }));
    expect(sentCommands()).toEqual([{ type: "resume" }]);

    await push({ queue: [] });
    expect(screen.queryByRole("group", { name: "Queued messages" })).toBeNull();
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

    const strip = screen.getByRole("group", { name: "Running sub-agents" });
    expect(within(strip).getByText("2 sub-agents running")).toBeTruthy();
    expect(
      within(strip)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual(["Explore repository2 tool calls", "Write tests0 tool calls"]);

    await push({
      messages: messages.map((message) => ("status" in message ? { ...message, status: "completed" } : message)),
    });
    expect(screen.queryByRole("group", { name: "Running sub-agents" })).toBeNull();
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
