import { Option, Schema } from "effect";
import { makeTerminalSessionController } from "../terminal/terminal-session.js";
import { ChatStateSchema, type ChatCommand, type ChatState } from "./chat-types.js";

const decodeFrame = Schema.decodeUnknownOption(
  Schema.fromJsonString(Schema.Union([ChatStateSchema, Schema.Struct({ commandError: Schema.String })])),
);

export function makeChatSession(options: {
  path: string;
  initialStatus: string;
  onState: (state: ChatState) => void;
  onError: (message: string) => void;
  onConnection: (connected: boolean) => void;
}) {
  const connection = makeTerminalSessionController({
    initialStatus: options.initialStatus,
    url: () => {
      const url = new URL(options.path, location.href);
      url.searchParams.set("protocol", "acp");
      url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
      return url.href;
    },
    onOpen: () => options.onConnection(true),
    onDisconnected: () => options.onConnection(false),
    onMessage: (data) => {
      if (typeof data !== "string") return "continue";
      const decoded = decodeFrame(data);
      if (Option.isNone(decoded)) {
        options.onError("The host sent an invalid chat response.");
        return "stop";
      }
      if ("commandError" in decoded.value) options.onError(decoded.value.commandError);
      else {
        options.onState(decoded.value);
        if (!decoded.value.connected) return "stop";
      }
      return "continue";
    },
  });
  return {
    program: connection.program,
    send(command: ChatCommand): boolean {
      if (!connection.isConnected()) return false;
      connection.send(JSON.stringify(command));
      return true;
    },
  };
}
