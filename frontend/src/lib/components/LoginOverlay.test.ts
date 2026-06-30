import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, test, vi } from "vite-plus/test";
import LoginOverlay from "./LoginOverlay.svelte";
import { loginHref } from "../api/auth-urls.js";

afterEach(cleanup);

describe("LoginOverlay", () => {
  test("renders the token field and hint", () => {
    render(LoginOverlay);
    expect(screen.getByLabelText("Access token")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
  });

  test("submitting a token navigates to the bootstrap URL", async () => {
    const navigate = vi.fn();
    render(LoginOverlay, { props: { navigate } });
    const input = screen.getByLabelText("Access token") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "  tok123  " } });
    await fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    const base = window.__BASE_PATH__ ?? "/";
    expect(navigate).toHaveBeenCalledWith(loginHref(base, "tok123"));
  });

  test("does not navigate on an empty token", async () => {
    const navigate = vi.fn();
    render(LoginOverlay, { props: { navigate } });
    await fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(navigate).not.toHaveBeenCalled();
  });
});
