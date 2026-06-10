import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, test, vi } from "vite-plus/test";
import DatePicker from "./DatePicker.svelte";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-06-01T12:00:00Z"));
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("DatePicker", () => {
  test("opens a tokenized calendar and reports an ISO date", async () => {
    const onchange = vi.fn();
    render(DatePicker, {
      props: {
        value: "2026-06-05",
        ariaLabel: "Due",
        onchange,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /Due:/ }));
    await fireEvent.click(screen.getByRole("button", { name: /Monday, June 8, 2026/ }));

    expect(onchange).toHaveBeenCalledWith("2026-06-08");
  });

  test("clear button reports an empty date", async () => {
    const onchange = vi.fn();
    render(DatePicker, {
      props: {
        value: "2026-06-05",
        ariaLabel: "Scheduled",
        clearable: true,
        onchange,
      },
    });

    await fireEvent.click(screen.getByRole("button", { name: /Clear scheduled/i }));

    expect(onchange).toHaveBeenCalledWith("");
  });

  test("Escape on clear button and calendar controls calls onEscape", async () => {
    const onEscape = vi.fn();
    render(DatePicker, {
      props: {
        value: "2026-06-05",
        ariaLabel: "Scheduled",
        clearable: true,
        onchange: vi.fn(),
        onEscape,
      },
    });

    await fireEvent.keyDown(screen.getByRole("button", { name: /Clear scheduled/i }), { key: "Escape" });
    expect(onEscape).toHaveBeenCalledTimes(1);

    await fireEvent.click(screen.getByRole("button", { name: /Scheduled:/ }));
    await fireEvent.keyDown(screen.getByRole("button", { name: "Next month" }), { key: "Escape" });

    expect(onEscape).toHaveBeenCalledTimes(2);
  });

  test("Escape bubbles when the picker is closed and has no escape handler", async () => {
    const onDocumentKeydown = vi.fn();
    document.addEventListener("keydown", onDocumentKeydown);
    try {
      render(DatePicker, {
        props: {
          value: "2026-06-05",
          ariaLabel: "Scheduled",
          clearable: true,
          onchange: vi.fn(),
        },
      });

      await fireEvent.keyDown(screen.getByRole("button", { name: /Clear scheduled/i }), { key: "Escape" });

      expect(onDocumentKeydown).toHaveBeenCalledTimes(1);
    } finally {
      document.removeEventListener("keydown", onDocumentKeydown);
    }
  });
});
