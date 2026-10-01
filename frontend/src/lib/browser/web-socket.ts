import { Effect, Layer } from "effect";
import { makeWebSocket, WebSocketConstructor } from "effect/socket/Socket";

export function makeWebSocketWithArrayBufferFrames<SocketType extends { binaryType: BinaryType }>(
  make: (url: string, protocols?: string | Array<string>) => SocketType,
  url: string,
  protocols?: string | Array<string>,
): SocketType {
  const socket = make(url, protocols);
  socket.binaryType = "arraybuffer";
  return socket;
}

export const WebSocketLive = Layer.succeed(WebSocketConstructor)((url, protocols) => {
  if (protocols !== undefined && typeof protocols !== "string" && !Array.isArray(protocols)) {
    throw new TypeError("WebSocket client options are not supported by the browser WebSocket constructor");
  }
  return makeWebSocketWithArrayBufferFrames(
    (socketUrl, socketProtocols) =>
      socketProtocols === undefined
        ? new globalThis.WebSocket(socketUrl)
        : new globalThis.WebSocket(socketUrl, socketProtocols),
    url,
    protocols,
  );
});

export const openWebSocket = Effect.fn("WebSocket.open")(function* (
  url: string,
  options?: Parameters<typeof makeWebSocket>[1],
) {
  return yield* makeWebSocket(url, options);
});
