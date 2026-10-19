import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { renderMarkdownSync } from "./markdown.js";
import { initMarkdownVideoToggle } from "./markdown-video-toggle.js";

let cleanup: (() => void) | undefined;

afterEach(() => {
  cleanup?.();
  cleanup = undefined;
  document.body.replaceChildren();
});

function mount(html: string): HTMLElement {
  const body = document.createElement("div");
  body.className = "markdown-body";
  body.innerHTML = html;
  document.body.append(body);
  cleanup = initMarkdownVideoToggle(document);
  return body;
}

describe("markdown video toggle", () => {
  it("collapses the player and pauses it, then expands it again", () => {
    const body = mount(renderMarkdownSync('<video src="https://cdn.example.com/demo.mp4"></video>'));
    const header = body.querySelector<HTMLButtonElement>(".markdown-video-header")!;
    const pause = vi.spyOn(body.querySelector("video")!, "pause").mockImplementation(() => {});

    header.querySelector(".markdown-video-name")!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(header.getAttribute("aria-expanded")).toBe("false");
    expect(pause).toHaveBeenCalledTimes(1);

    header.click();
    expect(header.getAttribute("aria-expanded")).toBe("true");
    expect(pause).toHaveBeenCalledTimes(1);
  });

  it("toggles only the header that was clicked", () => {
    const body = mount(
      renderMarkdownSync(
        '<video src="https://cdn.example.com/one.mp4"></video>\n\n<video src="https://cdn.example.com/two.mp4"></video>',
      ),
    );
    const [first, second] = body.querySelectorAll<HTMLButtonElement>(".markdown-video-header");

    second!.click();

    expect(first!.getAttribute("aria-expanded")).toBe("true");
    expect(second!.getAttribute("aria-expanded")).toBe("false");
  });

  it("leaves a button of the same class outside a player frame alone", () => {
    const body = mount('<button type="button" class="markdown-video-header" aria-expanded="true">Other</button>');

    body.querySelector("button")!.click();

    expect(body.querySelector("button")!.getAttribute("aria-expanded")).toBe("true");
  });
});
