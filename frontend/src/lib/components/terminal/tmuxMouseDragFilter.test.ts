import { describe, expect, it } from "vite-plus/test";

import { createTmuxMouseDragFilter } from "./tmuxMouseDragFilter";

const mouseDown = "\x1b[<0;10;5M";
const smallDrag = "\x1b[<32;12;5M";
const thresholdDrag = "\x1b[<32;13;5M";
const largeDrag = "\x1b[<32;14;5M";
const laterDrag = "\x1b[<32;15;5M";
const mouseUp = "\x1b[<0;14;5m";

function collect(filter: ReturnType<typeof createTmuxMouseDragFilter>, input: string[]): string {
  return input.map((chunk) => filter.filter(chunk)).join("");
}

describe("tmux mouse drag filter", () => {
  it("suppresses left-button drag reports until selection exceeds threshold", () => {
    const filter = createTmuxMouseDragFilter({ thresholdCells: 3 });

    const output = collect(filter, [mouseDown, smallDrag, thresholdDrag, mouseUp]);

    expect(output).toBe(mouseDown + mouseUp);
  });

  it("flushes buffered drag reports once selection exceeds threshold", () => {
    const filter = createTmuxMouseDragFilter({ thresholdCells: 3 });

    const output = collect(filter, [mouseDown, smallDrag, thresholdDrag, largeDrag, laterDrag, mouseUp]);

    expect(output).toBe(mouseDown + smallDrag + thresholdDrag + largeDrag + laterDrag + mouseUp);
  });

  it("passes through non-mouse terminal data around suppressed drag reports", () => {
    const filter = createTmuxMouseDragFilter({ thresholdCells: 3 });

    const output = filter.filter(`paste:${mouseDown}x${smallDrag}y${mouseUp}:done`);

    expect(output).toBe(`paste:${mouseDown}xy${mouseUp}:done`);
  });

  it("passes through Escape without waiting for more input", () => {
    const filter = createTmuxMouseDragFilter({ thresholdCells: 3 });

    expect(filter.filter("\x1b")).toBe("\x1b");
  });

  it("passes through wheel reports without applying drag threshold", () => {
    const filter = createTmuxMouseDragFilter({ thresholdCells: 3 });
    const wheelUp = "\x1b[<64;10;5M";

    expect(filter.filter(wheelUp)).toBe(wheelUp);
  });

  it("does not let wheel reports start drag filtering", () => {
    const filter = createTmuxMouseDragFilter({ thresholdCells: 3 });
    const wheelUp = "\x1b[<64;10;5M";
    const dragWithoutDown = "\x1b[<32;12;5M";

    expect(filter.filter(wheelUp + dragWithoutDown)).toBe(wheelUp + dragWithoutDown);
  });
});
