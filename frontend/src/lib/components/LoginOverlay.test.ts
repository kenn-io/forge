import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import LoginOverlay from "./LoginOverlay.svelte";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function submitToken(token: string): Promise<void> {
  const input = screen.getByLabelText("Access token") as HTMLInputElement;
  await fireEvent.input(input, { target: { value: token } });
  await fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("LoginOverlay", () => {
  test("renders the token field and hint", () => {
    render(LoginOverlay);
    expect(screen.getByLabelText("Access token")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
  });

  test("submitting a token POSTs it as JSON and reloads on success", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    const reload = vi.fn();
    render(LoginOverlay, { props: { reload } });

    await submitToken("  tok123  ");
    await waitFor(() => expect(reload).toHaveBeenCalled());

    const base = window.__BASE_PATH__ ?? "/";
    expect(fetchMock).toHaveBeenCalledWith(`${base}auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token: "tok123" }),
    });
  });

  test("a rejected token shows an error without reloading", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("invalid auth token", { status: 403 })));
    const reload = vi.fn();
    render(LoginOverlay, { props: { reload } });

    await submitToken("wrong");
    await screen.findByRole("alert");

    expect(screen.getByRole("alert").textContent).toBe("Invalid token");
    expect(reload).not.toHaveBeenCalled();
  });

  test("does not submit an empty token", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    render(LoginOverlay);
    await fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
