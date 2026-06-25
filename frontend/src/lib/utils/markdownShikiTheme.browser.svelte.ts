// Browser-tier coverage for markdown Shiki theme CSS. jsdom can assert the
// sanitized HTML shape, but only a real page proves app.css maps Shiki's light
// and dark CSS variables through the app's :root / :root.dark theme class.

import { afterEach, describe, expect, it } from "vite-plus/test";

import "../../app.css";
import { renderMarkdown } from "@middleman/ui/utils/markdown";

function mountedMarkdown(html: string): HTMLDivElement {
  const root = document.createElement("div");
  root.className = "markdown-body";
  root.innerHTML = html;
  document.body.append(root);
  return root;
}

describe("markdown Shiki theme styles (browser)", () => {
  afterEach(() => {
    document.querySelectorAll("[data-markdown-shiki-theme-test]").forEach((node) => node.remove());
    document.documentElement.classList.remove("dark");
  });

  it("switches highlighted code fences between light and dark app themes", async () => {
    const root = mountedMarkdown(await renderMarkdown('```toml\nmodel_provider = "my-custom"\n```'));
    root.dataset.markdownShikiThemeTest = "true";

    const pre = root.querySelector("pre.shiki");
    const token = root.querySelector("pre.shiki span[style]");
    expect(pre).toBeInstanceOf(HTMLElement);
    expect(token).toBeInstanceOf(HTMLElement);

    document.documentElement.classList.remove("dark");
    const lightPreStyle = getComputedStyle(pre as HTMLElement);
    const lightTokenStyle = getComputedStyle(token as HTMLElement);
    const lightBackground = lightPreStyle.backgroundColor;
    const lightPreColor = lightPreStyle.color;
    const lightTokenColor = lightTokenStyle.color;

    document.documentElement.classList.add("dark");
    const darkPreStyle = getComputedStyle(pre as HTMLElement);
    const darkTokenStyle = getComputedStyle(token as HTMLElement);

    expect(lightBackground).toBe("rgb(255, 255, 255)");
    expect(darkPreStyle.backgroundColor).toBe("rgb(13, 17, 23)");
    expect(darkPreStyle.color).not.toBe(lightPreColor);
    expect(darkTokenStyle.color).not.toBe(lightTokenColor);
  });
});
