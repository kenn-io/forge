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
