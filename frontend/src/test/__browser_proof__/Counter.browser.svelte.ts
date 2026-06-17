import { describe, expect, it } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import Counter from "./Counter.svelte";

describe("Counter (browser proof)", () => {
  it("increments the $state count on click in a real chromium page", async () => {
    render(Counter);

    const button = page.getByRole("button", { name: /count is/ });
    await expect.element(button).toHaveTextContent("count is 0");

    await button.click();
    await expect.element(button).toHaveTextContent("count is 1");

    await button.click();
    await expect.element(button).toHaveTextContent("count is 2");
  });
});
