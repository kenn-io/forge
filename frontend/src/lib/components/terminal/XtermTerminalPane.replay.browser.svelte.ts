import { Terminal } from "@xterm/xterm";
import { describe, expect, it } from "vite-plus/test";

function writeParsed(terminal: Terminal, data: string | Uint8Array): Promise<void> {
  return new Promise((resolve) => {
    terminal.write(data, resolve);
  });
}

describe("xterm terminal replay", () => {
  it("clears the decoder before replaying a split UTF-8 rune into the same renderer", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const terminal = new Terminal();
    terminal.open(target);

    try {
      // The old socket leaves a prefix in xterm's decoder. Disconnect writes
      // binary CAN, then the new subscriber replays that prefix before live
      // output supplies the continuation bytes.
      await writeParsed(terminal, new Uint8Array([0xe2]));
      await writeParsed(terminal, new Uint8Array([0x18]));
      await writeParsed(terminal, new Uint8Array([0xe2]));
      await writeParsed(terminal, new Uint8Array([0x98, 0x83, 0x41]));

      expect(terminal.buffer.active.getLine(0)?.translateToString(true)).toBe("☃A");
    } finally {
      terminal.dispose();
      target.remove();
    }
  });

  it("parses a live DECSET continuation after replay transitions", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const terminal = new Terminal();
    terminal.open(target);

    try {
      await writeParsed(terminal, "screen\x1b[?2004h\x1b[?1000");
      expect(terminal.modes.bracketedPasteMode).toBe(true);
      expect(terminal.modes.mouseTrackingMode).toBe("none");

      await writeParsed(terminal, "h");
      expect(terminal.modes.mouseTrackingMode).toBe("vt200");
    } finally {
      terminal.dispose();
      target.remove();
    }
  });

  it("drops raw C1 bytes but parses their valid UTF-8 encoding", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const terminal = new Terminal();
    terminal.open(target);

    try {
      await writeParsed(terminal, new Uint8Array([0x9b, 0x3f, 0x32, 0x30, 0x30, 0x34, 0x68]));
      expect(terminal.modes.bracketedPasteMode).toBe(false);

      await writeParsed(terminal, new Uint8Array([0xc2, 0x9b, 0x3f, 0x32, 0x30, 0x30, 0x34, 0x68]));
      expect(terminal.modes.bracketedPasteMode).toBe(true);
    } finally {
      terminal.dispose();
      target.remove();
    }
  });

  it("ignores DEC private-mode save and restore finals", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const terminal = new Terminal();
    terminal.open(target);

    try {
      await writeParsed(terminal, "\x1b[?1;1000;1004;2004h");
      expect(terminal.modes.applicationCursorKeysMode).toBe(true);
      expect(terminal.modes.mouseTrackingMode).toBe("vt200");
      expect(terminal.modes.sendFocusMode).toBe(true);
      expect(terminal.modes.bracketedPasteMode).toBe(true);

      await writeParsed(terminal, "\x1b[?1;1000;1004;2004s\x1b[?1;1000;1004;2004l\x1b[?1;1000;1004;2004r");
      expect(terminal.modes.applicationCursorKeysMode).toBe(false);
      expect(terminal.modes.mouseTrackingMode).toBe("none");
      expect(terminal.modes.sendFocusMode).toBe(false);
      expect(terminal.modes.bracketedPasteMode).toBe(false);
    } finally {
      terminal.dispose();
      target.remove();
    }
  });

  it("resets core modes but preserves the mouse service on DECSTR", async () => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const terminal = new Terminal();
    terminal.open(target);
    const reports: string[] = [];
    const dataSubscription = terminal.onData((data) => reports.push(data));

    try {
      await writeParsed(terminal, "\x1b[?1;1000;1004;1006;2004h");
      expect(terminal.modes.applicationCursorKeysMode).toBe(true);
      expect(terminal.modes.mouseTrackingMode).toBe("vt200");
      expect(terminal.modes.sendFocusMode).toBe(true);
      expect(terminal.modes.bracketedPasteMode).toBe(true);

      await writeParsed(terminal, "\x1b[!p");
      expect(terminal.modes.applicationCursorKeysMode).toBe(false);
      expect(terminal.modes.mouseTrackingMode).toBe("vt200");
      expect(terminal.modes.sendFocusMode).toBe(false);
      expect(terminal.modes.bracketedPasteMode).toBe(false);

      reports.length = 0;
      const screen = target.querySelector<HTMLElement>(".xterm-screen");
      expect(screen).not.toBeNull();
      const bounds = screen!.getBoundingClientRect();
      terminal.element!.dispatchEvent(
        new MouseEvent("mousedown", {
          bubbles: true,
          cancelable: true,
          button: 0,
          buttons: 1,
          clientX: bounds.left + 10,
          clientY: bounds.top + 10,
        }),
      );
      expect(reports.join("")).toMatch(/^\x1b\[<0;\d+;\d+M$/);
    } finally {
      dataSubscription.dispose();
      terminal.dispose();
      target.remove();
    }
  });

  it.each([
    ["ignored urxvt encoding after SGR", "\x1b[?1000;1006;1015h"],
    ["ignored urxvt encoding before SGR", "\x1b[?1000;1015;1006h"],
    ["ignored UTF-8 encoding after SGR pixels", "\x1b[?1000;1016;1005h"],
    ["ignored UTF-8 encoding before SGR pixels", "\x1b[?1000;1005;1016h"],
  ])("keeps %s", async (_name, sequence) => {
    const target = document.createElement("div");
    target.style.width = "600px";
    target.style.height = "400px";
    document.body.appendChild(target);
    const terminal = new Terminal();
    terminal.open(target);
    const reports: string[] = [];
    const dataSubscription = terminal.onData((data) => reports.push(data));

    try {
      await writeParsed(terminal, sequence);
      const screen = target.querySelector<HTMLElement>(".xterm-screen");
      expect(screen).not.toBeNull();
      const bounds = screen!.getBoundingClientRect();
      terminal.element!.dispatchEvent(
        new MouseEvent("mousedown", {
          bubbles: true,
          cancelable: true,
          button: 0,
          buttons: 1,
          clientX: bounds.left + 10,
          clientY: bounds.top + 10,
        }),
      );

      expect(reports.join("")).toMatch(/^\x1b\[<0;\d+;\d+M$/);
    } finally {
      dataSubscription.dispose();
      terminal.dispose();
      target.remove();
    }
  });
});
