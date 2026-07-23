import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import TerminalZoomControl from "./TerminalZoomControl.svelte";

describe("TerminalZoomControl", () => {
  afterEach(cleanup);

  it("exposes compact decrease, reset, and increase actions", async () => {
    const onDecrease = vi.fn();
    const onIncrease = vi.fn();
    const onReset = vi.fn();
    render(TerminalZoomControl, {
      props: {
        fontSize: 14,
        onDecrease,
        onIncrease,
        onReset,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: "Decrease terminal font size" }));
    await fireEvent.click(screen.getByRole("button", { name: "Reset terminal font size" }));
    await fireEvent.click(screen.getByRole("button", { name: "Increase terminal font size" }));

    expect(onDecrease).toHaveBeenCalledOnce();
    expect(onReset).toHaveBeenCalledOnce();
    expect(onIncrease).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: "Reset terminal font size" }).textContent).toContain("14px");
  });

  it("disables unavailable boundary actions", async () => {
    const { rerender } = render(TerminalZoomControl, {
      props: {
        fontSize: 8,
        onDecrease: vi.fn(),
        onIncrease: vi.fn(),
        onReset: vi.fn(),
      },
    });

    expect((screen.getByRole("button", { name: "Decrease terminal font size" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    expect((screen.getByRole("button", { name: "Increase terminal font size" }) as HTMLButtonElement).disabled).toBe(
      false,
    );

    await rerender({
      fontSize: 32,
      onDecrease: vi.fn(),
      onIncrease: vi.fn(),
      onReset: vi.fn(),
    });
    expect((screen.getByRole("button", { name: "Decrease terminal font size" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    expect((screen.getByRole("button", { name: "Increase terminal font size" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
  });
});
