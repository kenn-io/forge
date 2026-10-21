import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import { afterEach, expect, it, vi } from "vite-plus/test";
import QuerySearchInput from "./QuerySearchInput.svelte";

afterEach(cleanup);

it("submits the current query on Enter, excluding composition and key repeat", async () => {
  const onsubmit = vi.fn();
  render(QuerySearchInput, { props: { onsubmit } });
  const input = screen.getByRole("searchbox");
  await fireEvent.input(input, { target: { value: "missing query" } });
  await fireEvent.keyDown(input, { key: "Enter", isComposing: true });
  await fireEvent.keyDown(input, { key: "Enter", repeat: true });
  expect(onsubmit).not.toHaveBeenCalled();
  await fireEvent.keyDown(input, { key: "Enter" });
  expect(onsubmit).toHaveBeenCalledExactlyOnceWith("missing query");
});
