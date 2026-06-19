import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import UserPicker from "../../../../../packages/ui/src/components/detail/UserPicker.svelte";

describe("UserPicker", () => {
  afterEach(() => cleanup());

  it("lists candidates with selected users first and marks them checked", () => {
    render(UserPicker, {
      props: {
        title: "Edit assignees",
        candidates: ["alice", "bob"],
        selected: ["carol"],
        ontoggle: vi.fn(),
        onclose: vi.fn(),
      },
    });

    const rows = screen.getAllByRole("menuitemcheckbox");
    expect(rows.map((row) => row.textContent?.trim().split(/\s/)[0])).toEqual(["C", "A", "B"]);
    expect(screen.getByRole("menuitemcheckbox", { name: /carol/i }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByRole("menuitemcheckbox", { name: /alice/i }).getAttribute("aria-checked")).toBe("false");
  });

  it("filters users by the query", async () => {
    render(UserPicker, {
      props: {
        title: "Edit assignees",
        candidates: ["alice", "bob"],
        selected: [],
        ontoggle: vi.fn(),
        onclose: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Filter users"), {
      target: { value: "ali" },
    });

    expect(screen.queryByRole("menuitemcheckbox", { name: /bob/i })).toBeNull();
    expect(screen.getByRole("menuitemcheckbox", { name: /alice/i })).toBeTruthy();
  });

  it("dedupes selected users that also appear as candidates", () => {
    render(UserPicker, {
      props: {
        title: "Edit assignees",
        candidates: ["alice", "bob"],
        selected: ["alice"],
        ontoggle: vi.fn(),
        onclose: vi.fn(),
      },
    });

    expect(screen.getAllByRole("menuitemcheckbox")).toHaveLength(2);
  });

  it("renders synced profile images when an avatar URL is available", () => {
    render(UserPicker, {
      props: {
        title: "Edit reviewers",
        candidates: ["alice", "manual-user"],
        selected: [],
        avatarUrlForUser: (username) => (username === "alice" ? "https://github.example/alice.png?size=40" : ""),
        ontoggle: vi.fn(),
        onclose: vi.fn(),
      },
    });

    const aliceRow = screen.getByRole("menuitemcheckbox", { name: /alice/i });
    const aliceAvatar = aliceRow.querySelector("img.user-picker__avatar");
    expect(aliceAvatar?.getAttribute("src")).toBe("https://github.example/alice.png?size=40");
    expect(aliceAvatar?.getAttribute("alt")).toBe("");

    expect(screen.getByRole("menuitemcheckbox", { name: /manual-user/i }).textContent).toContain("M");
  });

  it("falls back to initials when a profile image fails to load", async () => {
    render(UserPicker, {
      props: {
        title: "Edit reviewers",
        candidates: ["roborev-ci"],
        selected: [],
        avatarUrlForUser: () => "https://github.example/roborev-ci.png?size=40",
        ontoggle: vi.fn(),
        onclose: vi.fn(),
      },
    });

    const row = screen.getByRole("menuitemcheckbox", { name: /roborev-ci/i });
    const avatar = row.querySelector("img.user-picker__avatar");
    expect(avatar).toBeTruthy();

    await fireEvent.error(avatar as HTMLImageElement);

    expect(row.querySelector("img.user-picker__avatar")).toBeNull();
    expect(row.textContent).toContain("R");
  });

  it("notifies query changes so callers can fetch matching candidates", async () => {
    const onQuery = vi.fn();
    render(UserPicker, {
      props: {
        title: "Edit assignees",
        candidates: ["alice"],
        selected: [],
        onquery: onQuery,
        ontoggle: vi.fn(),
        onclose: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Filter users"), {
      target: { value: "  zed " },
    });

    expect(onQuery).toHaveBeenCalledWith("zed");
  });

  it("offers an exact-username entry when the query matches no candidate", async () => {
    const onToggle = vi.fn();
    render(UserPicker, {
      props: {
        title: "Edit assignees",
        candidates: ["alice"],
        selected: ["bob"],
        ontoggle: onToggle,
        onclose: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Filter users"), {
      target: { value: "zed" },
    });
    await fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /add .zed./i }));
    expect(onToggle).toHaveBeenCalledWith("zed");

    // An exact match (even differing in case) suppresses the entry row.
    await fireEvent.input(screen.getByLabelText("Filter users"), {
      target: { value: "Alice" },
    });
    expect(screen.queryByRole("menuitemcheckbox", { name: /add .Alice./i })).toBeNull();
    await fireEvent.input(screen.getByLabelText("Filter users"), {
      target: { value: "bob" },
    });
    expect(screen.queryByRole("menuitemcheckbox", { name: /add .bob./i })).toBeNull();
  });

  it("withholds the exact-username entry until candidates reflect the typed query", async () => {
    const { rerender } = render(UserPicker, {
      props: {
        title: "Edit assignees",
        candidates: ["alice"],
        // The candidate list still reflects the previous query, so a
        // canonical-casing match for the new query may be in flight.
        candidatesQuery: "",
        selected: [],
        ontoggle: vi.fn(),
        onclose: vi.fn(),
      },
    });

    await fireEvent.input(screen.getByLabelText("Filter users"), {
      target: { value: "zed" },
    });
    expect(screen.queryByRole("menuitemcheckbox", { name: /add .zed./i })).toBeNull();

    await rerender({ candidates: ["zedmaster"], candidatesQuery: "zed" });
    expect(screen.getByRole("menuitemcheckbox", { name: /add .zed./i })).toBeTruthy();

    // A server result with canonical casing suppresses the entry row.
    await rerender({ candidates: ["Zed"], candidatesQuery: "zed" });
    expect(screen.queryByRole("menuitemcheckbox", { name: /add .zed./i })).toBeNull();
  });

  it("emits the toggled username", async () => {
    const onToggle = vi.fn();
    render(UserPicker, {
      props: {
        title: "Edit reviewers",
        candidates: ["alice"],
        selected: [],
        ontoggle: onToggle,
        onclose: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("menuitemcheckbox", { name: /alice/i }));

    expect(onToggle).toHaveBeenCalledWith("alice");
  });

  it("emits clear from the header action and disables rows while saving", async () => {
    const onClear = vi.fn();
    render(UserPicker, {
      props: {
        title: "Edit assignees",
        candidates: ["alice"],
        selected: ["alice"],
        pendingUser: null,
        ontoggle: vi.fn(),
        onclear: onClear,
        onclose: vi.fn(),
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Clear selected users" }));
    expect(onClear).toHaveBeenCalledOnce();

    cleanup();
    render(UserPicker, {
      props: {
        title: "Edit assignees",
        candidates: ["alice"],
        selected: [],
        pendingUser: "alice",
        ontoggle: vi.fn(),
        onclose: vi.fn(),
      },
    });
    expect(screen.getByRole("menuitemcheckbox", { name: /alice/i }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByText("Saving…")).toBeTruthy();
  });
});
