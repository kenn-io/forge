import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import MessagesSearchBar from "./MessagesSearchBar.svelte";
import type { MessagesCapabilities, MessagesSearchMode } from "../../api/messages/types";

afterEach(cleanup);

function makeCapabilities(modes: MessagesSearchMode[] = ["fts"]): MessagesCapabilities {
  return {
    configured: true,
    ok: true,
    status: "ok",
    modes,
    features: {
      threads_endpoint: true,
      mutations: false,
      attachments_download: false,
      sse_events: false,
    },
  };
}

describe("MessagesSearchBar", () => {
  test("initialQuery populates the input on mount", () => {
    render(MessagesSearchBar, {
      props: {
        capabilities: makeCapabilities(),
        initialQuery: "hello world",
        onSubmit: vi.fn(),
      },
    });
    const input = screen.getByRole("searchbox") as HTMLInputElement;
    expect(input.value).toBe("hello world");
  });

  test("submitting a non-empty query calls onSubmit with trimmed value", async () => {
    const onSubmit = vi.fn();
    render(MessagesSearchBar, {
      props: {
        capabilities: makeCapabilities(),
        initialQuery: "",
        onSubmit,
      },
    });

    const input = screen.getByRole("searchbox") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "  project plan  " } });
    await fireEvent.submit(screen.getByRole("search"));

    expect(onSubmit).toHaveBeenCalledOnce();
    expect(onSubmit).toHaveBeenCalledWith("project plan", "fts");
  });

  test("empty trimmed query submits as '' so the parent can clear route.q", async () => {
    const onSubmit = vi.fn();
    render(MessagesSearchBar, {
      props: {
        capabilities: makeCapabilities(),
        initialQuery: "previous",
        onSubmit,
      },
    });

    const input = screen.getByRole("searchbox") as HTMLInputElement;
    // Clearing the input then submitting IS the user's "clear the
    // query" gesture. The search bar reports an empty string and lets
    // the parent (MessagesWorkspace) decide whether it represents a
    // route-changing clear or a no-op. Short-circuiting on empty would
    // prevent the parent from ever seeing the clear.
    await fireEvent.input(input, { target: { value: "   " } });
    await fireEvent.submit(screen.getByRole("search"));

    expect(onSubmit).toHaveBeenCalledOnce();
    expect(onSubmit).toHaveBeenCalledWith("", "fts");
  });

  test("mode picker is disabled when only one mode is advertised", () => {
    render(MessagesSearchBar, {
      props: {
        capabilities: makeCapabilities(["fts"]),
        initialQuery: "",
        onSubmit: vi.fn(),
      },
    });

    const select = screen.getByRole("combobox", { name: "Search mode: fts" }) as HTMLButtonElement;
    expect(select.disabled).toBe(true);
  });

  test("mode picker is enabled and lists both options when two modes are advertised", async () => {
    render(MessagesSearchBar, {
      props: {
        capabilities: makeCapabilities(["fts", "vector"]),
        initialQuery: "",
        onSubmit: vi.fn(),
      },
    });

    const select = screen.getByRole("combobox", { name: "Search mode: fts" }) as HTMLButtonElement;
    expect(select.disabled).toBe(false);
    await fireEvent.click(select);

    expect(screen.getByRole("option", { name: "fts" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "vector" })).toBeTruthy();
  });

  test("mode picker passes the selected mode to onSubmit", async () => {
    const onSubmit = vi.fn();
    render(MessagesSearchBar, {
      props: {
        capabilities: makeCapabilities(["fts", "vector"]),
        initialQuery: "",
        onSubmit,
      },
    });

    const select = screen.getByRole("combobox", { name: "Search mode: fts" }) as HTMLButtonElement;
    await fireEvent.click(select);
    await fireEvent.click(screen.getByRole("option", { name: "vector" }));

    const input = screen.getByRole("searchbox") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "invoice" } });
    await fireEvent.submit(screen.getByRole("search"));

    expect(onSubmit).toHaveBeenCalledWith("invoice", "vector");
  });

  // Without this sync the input value freezes to the boot-time prop and
  // diverges from the URL after back/forward navigation or facet-driven
  // route changes - the list re-runs the new query while the search bar
  // still shows the old one.
  test("input re-syncs from initialQuery when the prop changes after mount", async () => {
    const { rerender } = render(MessagesSearchBar, {
      props: {
        capabilities: makeCapabilities(),
        initialQuery: "first",
        onSubmit: vi.fn(),
      },
    });

    const input = screen.getByRole("searchbox") as HTMLInputElement;
    expect(input.value).toBe("first");

    // Simulate popstate / facet click rewriting the URL: the parent passes a
    // new initialQuery prop and the input must reflect it.
    await rerender({
      capabilities: makeCapabilities(),
      initialQuery: "second",
      onSubmit: vi.fn(),
    });
    expect(input.value).toBe("second");

    // Clear case: route.q becomes null upstream -> initialQuery becomes "".
    await rerender({
      capabilities: makeCapabilities(),
      initialQuery: "",
      onSubmit: vi.fn(),
    });
    expect(input.value).toBe("");
  });
});
