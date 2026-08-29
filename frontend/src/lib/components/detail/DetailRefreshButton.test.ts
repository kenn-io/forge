import { fireEvent, render, screen } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vite-plus/test";
import DetailRefreshButton from "./DetailRefreshButton.svelte";

describe("DetailRefreshButton", () => {
  it("requests a detail refresh from an accessible icon button", async () => {
    const onRefresh = vi.fn();
    render(DetailRefreshButton, { props: { refreshing: false, onRefresh } });

    const button = screen.getByRole("button", { name: "Refresh detail" });
    expect((button as HTMLButtonElement).disabled).toBe(false);
    expect(button.getAttribute("title")).toBe("Refresh detail");

    await fireEvent.click(button);

    expect(onRefresh).toHaveBeenCalledOnce();
  });

  it("disables repeat refreshes and exposes the busy state", () => {
    render(DetailRefreshButton, { props: { refreshing: true, onRefresh: vi.fn() } });

    expect((screen.getByRole("button", { name: "Refresh detail" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByLabelText("Refreshing detail")).not.toBeNull();
  });

  it("can be disabled without presenting background work as a manual refresh", () => {
    render(DetailRefreshButton, {
      props: {
        disabled: true,
        disabledReason: "Hub unavailable",
        refreshing: false,
        onRefresh: vi.fn(),
      },
    });

    const button = screen.getByRole("button", { name: "Refresh detail" }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(button.title).toBe("Hub unavailable");
    expect(screen.queryByLabelText("Refreshing detail")).toBeNull();
  });
});
