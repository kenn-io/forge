import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import MessagesBanner from "./MessagesBanner.svelte";

afterEach(cleanup);

describe("MessagesBanner", () => {
  test("misconfigured: renders correct copy", () => {
    render(MessagesBanner, { props: { status: "misconfigured" } });
    const el = screen.getByRole("alert");
    expect(el.textContent).toContain("Messages are misconfigured - check message source settings");
  });

  test("misconfigured: renders statusDetail in parens when provided", () => {
    render(MessagesBanner, {
      props: { status: "misconfigured", statusDetail: "missing api_key_env" },
    });
    const el = screen.getByRole("alert");
    expect(el.textContent).toContain(
      "Messages are misconfigured - check message source settings (missing api_key_env)",
    );
  });

  test("down: renders correct copy", () => {
    render(MessagesBanner, { props: { status: "down" } });
    const el = screen.getByRole("alert");
    expect(el.textContent).toContain("Messages unavailable - retrying on next refresh.");
  });

  test("unauthorized: renders correct copy", () => {
    render(MessagesBanner, { props: { status: "unauthorized" } });
    const el = screen.getByRole("alert");
    expect(el.textContent).toContain("Messages key rejected - check `api_key_env`.");
  });

  test("Configure button is absent when onConfigure is undefined", () => {
    render(MessagesBanner, { props: { status: "misconfigured" } });
    expect(screen.queryByRole("button", { name: "Configure messages" })).toBeNull();
  });

  test("Configure button is present when onConfigure is provided", () => {
    render(MessagesBanner, {
      props: { status: "misconfigured", onConfigure: () => {} },
    });
    const button = screen.getByRole("button", { name: "Configure messages" });
    expect(button).toBeTruthy();
    expect(button.getAttribute("aria-label")).toBe("Configure messages");
    expect(button.textContent).toContain("Configure");
  });

  test("clicking Configure fires the callback", async () => {
    const onConfigure = vi.fn();
    render(MessagesBanner, { props: { status: "misconfigured", onConfigure } });
    const button = screen.getByRole("button", { name: "Configure messages" });
    await fireEvent.click(button);
    expect(onConfigure).toHaveBeenCalledOnce();
  });

  test("Configure button renders for down/unauthorized too when onConfigure is provided", () => {
    for (const status of ["down", "unauthorized"] as const) {
      cleanup();
      render(MessagesBanner, { props: { status, onConfigure: () => {} } });
      expect(screen.getByRole("button", { name: "Configure messages" })).toBeTruthy();
    }
  });
});
