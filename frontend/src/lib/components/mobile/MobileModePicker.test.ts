import { fireEvent, render, screen } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vite-plus/test";
import MobileModePicker from "./MobileModePicker.svelte";

describe("MobileModePicker", () => {
  function pickerValue(): string {
    return screen.getByRole("combobox", { name: /Phone mode/ }).textContent?.trim() ?? "";
  }

  it("selects the workspace family for workspace terminal and item routes", () => {
    const { rerender } = render(MobileModePicker, {
      props: {
        page: "mobile-workspace-terminal",
        isModeVisible: () => true,
        onNavigate: vi.fn(),
      },
    });

    expect(pickerValue()).toContain("Workspaces");

    return rerender({
      page: "mobile-workspace-item",
      isModeVisible: () => true,
      onNavigate: vi.fn(),
    }).then(() => {
      expect(pickerValue()).toContain("Workspaces");
    });
  });

  it("navigates and excludes hidden modes", async () => {
    const onNavigate = vi.fn();
    render(MobileModePicker, {
      props: {
        page: "mobile-workspaces",
        isModeVisible: (mode) => mode !== "issues",
        onNavigate,
      },
    });

    const picker = screen.getByRole("combobox", { name: /Phone mode/ });
    await fireEvent.click(picker);
    expect(screen.queryByRole("option", { name: "Issues" })).toBeNull();
    await fireEvent.click(screen.getByRole("option", { name: "PRs" }));
    expect(onNavigate).toHaveBeenCalledWith("/m/pulls");
  });
});
