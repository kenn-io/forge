import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import * as flash from "@middleman/ui/stores/flash";
import UserListEditor from "../../../../../packages/ui/src/components/detail/UserListEditor.svelte";

describe("UserListEditor", () => {
  afterEach(() => {
    cleanup();
    for (const item of flash.getFlashes()) flash.dismissFlash(item.id);
  });

  it("keeps a mutation flash visible when a later candidate fetch succeeds", async () => {
    const loadCandidates = vi.fn().mockResolvedValue(["alice", "bob"]);
    const onchange = vi.fn().mockRejectedValue(new Error("provider rejected the save"));
    render(UserListEditor, {
      props: {
        label: "Assignees",
        users: [],
        canEdit: true,
        loadCandidates,
        onchange,
      },
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

  it("closes the picker and blocks mutations once the view goes stale", async () => {
    const onchange = vi.fn();
    const { rerender } = render(UserListEditor, {
      props: {
        label: "Assignees",
        users: ["alice"],
        canEdit: true,
        disabled: false,
        loadCandidates: vi.fn().mockResolvedValue(["alice", "bob"]),
        onchange,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(screen.getByRole("dialog", { name: "Edit assignees" })).toBeTruthy());

    // The item went stale (e.g. navigation): the open picker must
    // close so it cannot mutate whatever the handlers now target.
    await rerender({ disabled: true });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Edit assignees" })).toBeNull());
    expect(onchange).not.toHaveBeenCalled();
  });

  it("clears a non-empty filter on the first Escape and closes the picker on the second", async () => {
    render(UserListEditor, {
      props: {
        label: "Assignees",
        users: [],
        canEdit: true,
        loadCandidates: vi.fn().mockResolvedValue(["alice", "bob"]),
        onchange: vi.fn(),
      },
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
    render(UserListEditor, {
      props: {
        label: "Assignees",
        users: [],
        canEdit: true,
        loadCandidates: vi.fn().mockResolvedValue(["alice"]),
        onchange: vi.fn(),
      },
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
      loadCandidates: vi.fn().mockResolvedValue(["alice"]),
      onchange: vi.fn(),
    };
    render(UserListEditor, { props: { ...props, label: "Assignees" } });
    render(UserListEditor, { props: { ...props, label: "Reviewers" } });

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
      loadCandidates: vi.fn().mockResolvedValue(["alice"]),
      onchange: vi.fn(),
    };
    render(UserListEditor, { props: { ...props, label: "Assignees" } });
    render(UserListEditor, { props: { ...props, label: "Reviewers" } });

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
      .mockRejectedValueOnce(new Error("failed to load users"))
      .mockResolvedValue(["carol"]);
    render(UserListEditor, {
      props: {
        label: "Assignees",
        users: [],
        canEdit: true,
        loadCandidates,
        onchange: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Edit assignees" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("failed to load users"));

    await fireEvent.input(screen.getByLabelText("Filter users"), { target: { value: "car" } });
    await waitFor(() => expect(screen.getByRole("menuitemcheckbox", { name: /carol/i })).toBeTruthy(), {
      timeout: 2000,
    });
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
