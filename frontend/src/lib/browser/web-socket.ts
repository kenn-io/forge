import { BrowserSocket } from "@effect/platform-browser";
import { Effect } from "effect";
import { makeWebSocket } from "effect/unstable/socket/Socket";

export const WebSocketLive = BrowserSocket.layerWebSocketConstructor;

export const openWebSocket = Effect.fn("WebSocket.open")(function* (
  url: string,
  options?: Parameters<typeof makeWebSocket>[1],
) {
  return yield* makeWebSocket(url, {
    ...options,
    closeCodeIsError: options?.closeCodeIsError ?? ((code) => code !== 1000),
  });
});
