import { describe, expect, it } from "vite-plus/test";
import { decodeTerminalControlMessage } from "./terminal-control-message.js";

describe("terminal control messages", () => {
  it("decodes authoritative workspace deletion", () => {
    expect(decodeTerminalControlMessage('{"type":"workspace_deleted"}')).toEqual({
      type: "workspace_deleted",
    });
  });

  it("rejects malformed lifecycle messages", () => {
    expect(decodeTerminalControlMessage('{"type":"exited","code":"zero"}')).toBeNull();
    expect(decodeTerminalControlMessage('{"type":"unknown"}')).toBeNull();
  });
});
