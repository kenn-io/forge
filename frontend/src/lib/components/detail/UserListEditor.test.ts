import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { Effect } from "effect";
import type { ComponentProps } from "svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { makeAppRuntime, type OwnedAppRuntime } from "../../app/runtime.js";
import * as flash from "../../stores/flash.svelte.js";
import UserListEditor from "./UserListEditor.svelte";
import UserListEditorTestHarness from "./UserListEditorTestHarness.svelte";

let runtime: OwnedAppRuntime;

function renderEditor(editorProps: ComponentProps<typeof UserListEditor>) {
  const rendered = render(UserListEditorTestHarness, { props: { runtime, editorProps } });
  return {
    ...rendered,
    rerenderEditor: (next: Partial<ComponentProps<typeof UserListEditor>>) => {
      editorProps = { ...editorProps, ...next };
      return rendered.rerender({ runtime, editorProps });
    },
  };
}

function candidateLoader(values: string[]) {
  return vi.fn(() => Effect.succeed(values));
}

describe("UserListEditor", () => {
  beforeEach(() => {
    runtime = makeAppRuntime();
  });

  afterEach(async () => {
    cleanup();
    for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
    await Effect.runPromise(runtime.disposeEffect);
  });

  it("keeps a mutation flash visible when a later candidate fetch succeeds", async () => {
    const loadCandidates = candidateLoader(["alice", "bob"]);
    const onchange = vi.fn((_next, callbacks) => {
      flash.showFlash("provider rejected the save", { tone: "danger" });
      callbacks.onSettled?.();
    });
    renderEditor({
      label: "Assignees",
      users: [],
      canEdit: true,
      loadCandidates,
      onchange,
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(screen.getByRole("menuitemcheckbox", { name: /alice/i })).toBeTruthy());

    await fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /alice/i }));
    await waitFor(() => {
      expect(flash.getFlash()).toMatchObject({
        message: "provider rejected the save",
        tone: "danger",
      });
    });
    expect(screen.queryByRole("alert")).toBeNull();

    // Typing re-queries candidates; the successful fetch must not
    // clear the still-unresolved mutation error.
    await fireEvent.input(screen.getByLabelText("Filter users"), { target: { value: "bo" } });
    await waitFor(() => expect(loadCandidates).toHaveBeenCalledWith("bo"), { timeout: 2000 });
    await waitFor(() => expect(screen.getByRole("menuitemcheckbox", { name: /bob/i })).toBeTruthy());
    expect(flash.getFlash()).toMatchObject({
      message: "provider rejected the save",
      tone: "danger",
    });
  });

  it("keeps one synchronous mutation pending until its acknowledgement settles", async () => {
    let settle = () => {};
    const onchange = vi.fn((_next, callbacks) => {
      settle = () => callbacks.onSettled?.();
    });
    renderEditor({
      label: "Assignees",
      users: [],
      canEdit: true,
      loadCandidates: candidateLoader(["alice", "bob"]),
      onchange,
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(screen.getByRole("menuitemcheckbox", { name: /alice/i })).toBeTruthy());
    await fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /alice/i }));
    await fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /bob/i }));
    expect(onchange).toHaveBeenCalledTimes(1);

    settle();
    await fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /bob/i }));
    expect(onchange).toHaveBeenCalledTimes(2);
  });

  it("closes the picker and blocks mutations once the view goes stale", async () => {
    const onchange = vi.fn();
    const { rerenderEditor } = renderEditor({
      label: "Assignees",
      users: ["alice"],
      canEdit: true,
      disabled: false,
      loadCandidates: candidateLoader(["alice", "bob"]),
      onchange,
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(screen.getByRole("dialog", { name: "Edit assignees" })).toBeTruthy());

    // The item went stale (e.g. navigation): the open picker must
    // close so it cannot mutate whatever the handlers now target.
    await rerenderEditor({ disabled: true });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit assignees" })).toBeNull());
    expect(onchange).not.toHaveBeenCalled();
  });

  it("clears a non-empty filter on the first Escape and closes the picker on the second", async () => {
    renderEditor({
      label: "Assignees",
      users: [],
      canEdit: true,
      loadCandidates: candidateLoader(["alice", "bob"]),
      onchange: vi.fn(),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(screen.getByRole("dialog", { name: "Edit assignees" })).toBeTruthy());

    const input = screen.getByLabelText("Filter users") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "ali" } });
    expect(input.value).toBe("ali");

    // Non-empty field: Escape clears the filter, the picker stays open.
    await fireEvent.keyDown(input, { key: "Escape" });
    expect(input.value).toBe("");
    expect(screen.getByRole("dialog", { name: "Edit assignees" })).toBeTruthy();

    // Empty field: Escape bubbles to the popover host and dismisses it.
    await fireEvent.keyDown(input, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit assignees" })).toBeNull());
  });

  it("dismisses the picker on a press outside the chip and panel", async () => {
    renderEditor({
      label: "Assignees",
      users: [],
      canEdit: true,
      loadCandidates: candidateLoader(["alice"]),
      onchange: vi.fn(),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(screen.getByRole("dialog", { name: "Edit assignees" })).toBeTruthy());

    // A press inside the panel must not dismiss it.
    await fireEvent.mouseDown(screen.getByLabelText("Filter users"));
    expect(screen.getByRole("dialog", { name: "Edit assignees" })).toBeTruthy();

    await fireEvent.mouseDown(document.body);
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit assignees" })).toBeNull());
  });

  it("closes an open picker when another editor's chip is pressed", async () => {
    const props = {
      users: [],
      canEdit: true,
      loadCandidates: candidateLoader(["alice"]),
      onchange: vi.fn(),
    };
    renderEditor({ ...props, label: "Assignees" });
    renderEditor({ ...props, label: "Reviewers" });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(screen.getByRole("dialog", { name: "Edit assignees" })).toBeTruthy());

    // A real pointer press fires mousedown before click; both pickers
    // must never be on screen together.
    const reviewersChip = screen.getByRole("button", { name: "Edit reviewers" });
    await fireEvent.mouseDown(reviewersChip);
    await fireEvent.click(reviewersChip);

    await waitFor(() => expect(screen.getByRole("dialog", { name: "Edit reviewers" })).toBeTruthy());
    expect(screen.queryByRole("dialog", { name: "Edit assignees" })).toBeNull();
  });

  it("closes an open picker when another editor's chip is activated by keyboard", async () => {
    const props = {
      users: [],
      canEdit: true,
      loadCandidates: candidateLoader(["alice"]),
      onchange: vi.fn(),
    };
    renderEditor({ ...props, label: "Assignees" });
    renderEditor({ ...props, label: "Reviewers" });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(screen.getByRole("dialog", { name: "Edit assignees" })).toBeTruthy());

    // Enter/Space on a button dispatches only a click — no mousedown —
    // so this must be handled by the shared open-picker slot, not the
    // document-mousedown dismissal.
    await fireEvent.click(screen.getByRole("button", { name: "Edit reviewers" }));

    await waitFor(() => expect(screen.getByRole("dialog", { name: "Edit reviewers" })).toBeTruthy());
    expect(screen.queryByRole("dialog", { name: "Edit assignees" })).toBeNull();
  });

  it("clears a candidate-load error once a later fetch succeeds", async () => {
    const loadCandidates = vi
      .fn()
      .mockReturnValueOnce(Effect.fail(new Error("failed to load users")))
      .mockReturnValue(Effect.succeed(["carol"]));
    renderEditor({
      label: "Assignees",
      users: [],
      canEdit: true,
      loadCandidates,
      onchange: vi.fn(),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("failed to load users"));

    await fireEvent.input(screen.getByLabelText("Filter users"), { target: { value: "car" } });
    await waitFor(() => expect(screen.getByRole("menuitemcheckbox", { name: /carol/i })).toBeTruthy(), {
      timeout: 2000,
    });
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("interrupts candidate loading when the picker closes", async () => {
    let interrupted = false;
    const loadCandidates = vi.fn(() =>
      Effect.never.pipe(
        Effect.onInterrupt(() =>
          Effect.sync(() => {
            interrupted = true;
          }),
        ),
      ),
    );
    renderEditor({
      label: "Assignees",
      users: [],
      canEdit: true,
      loadCandidates,
      onchange: vi.fn(),
    });

    const trigger = screen.getByRole("button", { name: "Edit assignees" });
    await fireEvent.click(trigger);
    await waitFor(() => expect(loadCandidates).toHaveBeenCalled());
    await fireEvent.click(trigger);

    await waitFor(() => expect(interrupted).toBe(true));
  });

  it("interrupts candidate loading when the editor unmounts", async () => {
    let interrupted = false;
    const loadCandidates = vi.fn(() =>
      Effect.never.pipe(
        Effect.onInterrupt(() =>
          Effect.sync(() => {
            interrupted = true;
          }),
        ),
      ),
    );
    const { unmount } = renderEditor({
      label: "Assignees",
      users: [],
      canEdit: true,
      loadCandidates,
      onchange: vi.fn(),
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(loadCandidates).toHaveBeenCalled());
    unmount();

    await waitFor(() => expect(interrupted).toBe(true));
  });
});
