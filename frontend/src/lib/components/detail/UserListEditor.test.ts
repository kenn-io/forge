import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import UserListEditor from "../../../../../packages/ui/src/components/detail/UserListEditor.svelte";

describe("UserListEditor", () => {
  afterEach(() => cleanup());

  it("keeps a mutation error visible when a later candidate fetch succeeds", async () => {
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
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("provider rejected the save"));

    // Typing re-queries candidates; the successful fetch must not
    // clear the still-unresolved mutation error.
    await fireEvent.input(screen.getByLabelText("Filter users"), { target: { value: "bo" } });
    await waitFor(() => expect(loadCandidates).toHaveBeenCalledWith("bo"), { timeout: 2000 });
    await waitFor(() => expect(screen.getByRole("menuitemcheckbox", { name: /bob/i })).toBeTruthy());
    expect(screen.getByRole("alert").textContent).toContain("provider rejected the save");
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
