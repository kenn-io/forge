import { describe, expect, it, vi } from "vite-plus/test";

import { createOsc52OutputFilter } from "./osc52OutputFilter";

const encoder = new TextEncoder();
const decoder = new TextDecoder();
const CAN = "\x18";

function bytes(text: string): Uint8Array {
  return encoder.encode(text);
}

function text(data: Uint8Array): string {
  return decoder.decode(data);
}

describe("OSC 52 output filter", () => {
  it("consumes OSC 52 while preserving surrounding terminal output", () => {
    const onOsc52 = vi.fn();
    const filter = createOsc52OutputFilter(onOsc52);

    const output = filter.write(bytes("before\x1b]52;c;Y29waWVkIHRleHQ=\x07after"));

    expect(text(output)).toBe(`before${CAN}after`);
    expect(onOsc52).toHaveBeenCalledWith("c;Y29waWVkIHRleHQ=");
  });

  it("reassembles OSC 52 split across output chunks", () => {
    const onOsc52 = vi.fn();
    const filter = createOsc52OutputFilter(onOsc52);

    expect(text(filter.write(bytes("before\x1b")))).toBe("before");
    expect(text(filter.write(bytes("]52;c;Y29w")))).toBe(CAN);
    expect(text(filter.write(bytes("aWVk\x1b\\after")))).toBe("after");

    expect(onOsc52).toHaveBeenCalledWith("c;Y29waWVk");
  });

  it("preserves non-clipboard OSC sequences across chunks", () => {
    const onOsc52 = vi.fn();
    const filter = createOsc52OutputFilter(onOsc52);

    expect(text(filter.write(bytes("before\x1b]0;title")))).toBe("before\x1b]0;title");
    expect(text(filter.write(bytes("\x07after")))).toBe("\x07after");
    expect(onOsc52).not.toHaveBeenCalled();
  });

  it("recognizes OSC 52 after unterminated unrelated OSC output", () => {
    const onOsc52 = vi.fn();
    const filter = createOsc52OutputFilter(onOsc52);

    const output = filter.write(bytes("\x1b]133;A\x1b]52;c;Y29waWVkIHRleHQ=\x07visible"));

    expect(text(output)).toBe(`\x1b]133;A${CAN}visible`);
    expect(onOsc52).toHaveBeenCalledWith("c;Y29waWVkIHRleHQ=");
  });

  it.each([
    { introducer: [0x1b, 0x5d], name: "ESC", prefix: "5" },
    { introducer: [0x1b, 0x5d], name: "ESC", prefix: "52" },
    { introducer: [0x9d], name: "C1", prefix: "5" },
    { introducer: [0x9d], name: "C1", prefix: "52" },
  ])("recognizes nested $name OSC 52 after command prefix $prefix", ({ introducer, prefix }) => {
    const onOsc52 = vi.fn();
    const filter = createOsc52OutputFilter(onOsc52);

    const output = filter.write(
      Uint8Array.from([...bytes(`\x1b]${prefix}`), ...introducer, ...bytes("52;c;Y29waWVkIHRleHQ=\x07visible")]),
    );

    expect(text(output)).toBe(`\x1b]${prefix}${CAN}visible`);
    expect(onOsc52).toHaveBeenCalledWith("c;Y29waWVkIHRleHQ=");
  });

  it("recognizes C1 OSC immediately after an ESC prefix", () => {
    const onOsc52 = vi.fn();
    const filter = createOsc52OutputFilter(onOsc52);

    const output = filter.write(Uint8Array.from([0x1b, 0x9d, ...bytes("52;c;Y29waWVkIHRleHQ=\x07visible")]));

    expect(Array.from(output)).toEqual([0x1b, 0x18, ...bytes("visible")]);
    expect(onOsc52).toHaveBeenCalledWith("c;Y29waWVkIHRleHQ=");
  });

  it.each([
    { name: "7-bit", introducer: [0x1b, 0x5d] },
    { name: "C1", introducer: [0x9d] },
  ])("filters leading-zero OSC 52 with the $name introducer", ({ introducer }) => {
    const onOsc52 = vi.fn();
    const filter = createOsc52OutputFilter(onOsc52);

    const output = filter.write(Uint8Array.from([...introducer, ...bytes("052;c;Y29waWVk\x07visible")]));

    expect(text(output)).toBe(`${CAN}visible`);
    expect(onOsc52).toHaveBeenCalledWith("c;Y29waWVk");
  });

  it.each([
    { name: "5", prefix: "\x1b]5", suffix: "2;c;b3V0ZXI=\x07" },
    { name: "52", prefix: "\x1b]52", suffix: ";c;b3V0ZXI=\x07" },
    { name: "ESC", prefix: "\x1b", suffix: "]52;c;b3V0ZXI=\x07" },
  ])("prevents a removed OSC 52 from completing a preserved $name prefix", ({ prefix, suffix }) => {
    const onOsc52 = vi.fn();
    const filter = createOsc52OutputFilter(onOsc52);
    const downstreamOsc52 = vi.fn();
    const downstream = createOsc52OutputFilter(downstreamOsc52);

    downstream.write(filter.write(bytes(`${prefix}\x1b]52;c;aW5uZXI=\x07`)));
    downstream.write(filter.write(bytes(suffix)));

    expect(onOsc52).toHaveBeenCalledOnce();
    expect(onOsc52).toHaveBeenCalledWith("c;aW5uZXI=");
    expect(downstreamOsc52).not.toHaveBeenCalled();
  });

  it("cancels a preserved prefix before an incomplete nested OSC 52 is reset", () => {
    const filter = createOsc52OutputFilter(vi.fn());
    const downstreamOsc52 = vi.fn();
    const downstream = createOsc52OutputFilter(downstreamOsc52);

    downstream.write(filter.write(bytes("\x1b]5\x1b]52;c;aW5jb21wbGV0ZQ==")));
    filter.reset();
    downstream.write(filter.write(bytes("2;c;b3V0ZXI=\x07")));

    expect(downstreamOsc52).not.toHaveBeenCalled();
  });

  it("consumes oversized OSC 52 without retaining or dispatching it", () => {
    const onOsc52 = vi.fn();
    const filter = createOsc52OutputFilter(onOsc52, { maxDataBytes: 8 });

    const output = filter.write(bytes("\x1b]52;c;Y29waWVkIHRleHQ=\x07visible"));

    expect(text(output)).toBe(`${CAN}visible`);
    expect(onOsc52).not.toHaveBeenCalled();
  });
});
