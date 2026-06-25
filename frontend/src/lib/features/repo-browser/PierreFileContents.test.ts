import { render, screen } from "@testing-library/svelte";
import { beforeEach, describe, expect, it, vi } from "vite-plus/test";

const pierreMock = vi.hoisted(() => ({
  render: vi.fn(),
  setOptions: vi.fn(),
  setThemeType: vi.fn(),
  cleanUp: vi.fn(),
}));

vi.mock("@pierre/diffs", () => ({
  File: class {
    setOptions(...args: unknown[]) {
      return pierreMock.setOptions(...args);
    }

    render(...args: unknown[]) {
      return pierreMock.render(...args);
    }

    setThemeType(...args: unknown[]) {
      return pierreMock.setThemeType(...args);
    }

    cleanUp(...args: unknown[]) {
      return pierreMock.cleanUp(...args);
    }
  },
}));

import PierreFileContents from "./PierreFileContents.svelte";

describe("PierreFileContents", () => {
  beforeEach(() => {
    pierreMock.render.mockReset();
    pierreMock.setOptions.mockReset();
    pierreMock.setThemeType.mockReset();
    pierreMock.cleanUp.mockReset();
  });

  it("falls back to plaintext when Pierre rendering throws", async () => {
    pierreMock.render.mockImplementation(() => {
      throw new Error("render failed");
    });

    render(PierreFileContents, {
      props: {
        path: "src/main.ts",
        contents: "export const value = 1;\n",
      },
    });

    const fallback = await screen.findByTestId("repo-browser-plaintext-file-contents");

    expect(fallback.textContent).toBe("export const value = 1;\n");
    expect(pierreMock.cleanUp).toHaveBeenCalled();
  });
});
