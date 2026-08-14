import { describe, expect, it } from "vite-plus/test";
import { decodeTerminalControlMessage } from "./terminal-control-message.js";

describe("terminal control messages", () => {
  it("decodes terminal lifecycle messages", () => {
    expect(decodeTerminalControlMessage('{"type":"exited","code":3}')).toEqual({ type: "exited", code: 3 });
    expect(decodeTerminalControlMessage('{"type":"replay_ready"}')).toEqual({ type: "replay_ready" });
  });

  it("rejects malformed lifecycle messages", () => {
    expect(decodeTerminalControlMessage('{"type":"exited","code":"zero"}')).toBeNull();
    expect(decodeTerminalControlMessage('{"type":"unknown"}')).toBeNull();
  });
});
