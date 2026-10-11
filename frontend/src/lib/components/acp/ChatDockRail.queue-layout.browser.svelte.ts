// Guards the queue panel layout with a coordinator's prompt queued: a long
// coordinator name must never squeeze out the message text or push the move
// and remove buttons out of the panel. Narrow panes show only "Coordinator";
// wide panes show the name, so they are where a long one does harm. Rects and
// truncation are only real in a browser.

import { afterEach, describe, expect, it } from "vite-plus/test";
import { cleanup, render } from "vitest-browser-svelte";

// Layout assertions are only meaningful under the production reset and tokens.
import "../../../app.css";
import ChatDockRail from "./ChatDockRail.svelte";

const longName = "release-coordinator-for-the-entire-organization-and-every-repository-it-owns";

// The largest ACP font setting the schema allows.
const maxACPFont = 32;

async function mountWide(widthPx: number) {
  const pane = document.createElement("div");
  // ACPWorkspace makes the chat a size container named acp-pane and derives
  // the rail's type tokens from the ACP font; these are its desktop values.
  pane.style.width = `${widthPx}px`;
  pane.style.setProperty("container", "acp-pane / inline-size");
  pane.style.setProperty("--font-size-acp", `${maxACPFont}px`);
  pane.style.setProperty("--font-size-xs", `${(maxACPFont * 11) / 13}px`);
  pane.style.setProperty("--font-size-sm", `${(maxACPFont * 12) / 13}px`);
  document.body.appendChild(pane);
  const result = await render(ChatDockRail, {
    target: pane,
    props: {
      plan: [],
      subagents: [],
      queue: [
        {
          id: "a",
          text: "Run the release checklist for the next patch version",
          sender: { kind: "coordinator", name: longName },
        },
        { id: "b", text: "Also update the changelog", sender: { kind: "user" } },
      ],
      queuePaused: false,
      disabled: false,
      onresume: () => {},
      onunqueue: () => {},
      onmove: () => {},
    },
  });
  (pane.querySelector("button[aria-controls$='-queue']") as HTMLButtonElement).click();
  await new Promise((resolve) => requestAnimationFrame(resolve));
  return { pane, result };
}

afterEach(() => {
  cleanup();
  document.body.replaceChildren();
});

describe("ChatDockRail queue layout", () => {
  // Any pane wider than the 40rem breakpoint shows the name; the panel itself
  // stops at 36rem, so one wide pane covers them all.
  it("keeps the message and its buttons visible beside a long coordinator name at the largest ACP font", async () => {
    const { pane } = await mountWide(900);
    const panel = pane.querySelector(".panel") as HTMLElement;
    const row = pane.querySelector(".row") as HTMLElement;
    expect(parseFloat(getComputedStyle(row).fontSize)).toBeCloseTo((maxACPFont * 11) / 13, 1);
    const sender = row.querySelector(".row-sender") as HTMLElement;
    const text = row.querySelector(".row-text") as HTMLElement;
    const actions = [...row.querySelectorAll(".row-action")] as HTMLElement[];
    expect(sender.textContent).toContain(longName);

    const panelBox = panel.getBoundingClientRect();
    const rowBox = row.getBoundingClientRect();
    expect(sender.getBoundingClientRect().width).toBeLessThanOrEqual(rowBox.width * 0.4 + 1);
    expect(text.getBoundingClientRect().width).toBeGreaterThan(rowBox.width * 0.25);
    expect(actions).toHaveLength(3);
    for (const action of actions) {
      const box = action.getBoundingClientRect();
      expect(box.width).toBeGreaterThan(0);
      expect(box.right).toBeLessThanOrEqual(panelBox.right + 1);
    }
  });
});
