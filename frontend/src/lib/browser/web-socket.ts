import { Effect, Layer } from "effect";
import { makeWebSocket, WebSocketConstructor } from "effect/unstable/socket/Socket";

export function makeWebSocketWithArrayBufferFrames<SocketType extends { binaryType: BinaryType }>(
  make: (url: string, protocols?: string | Array<string>) => SocketType,
  url: string,
  protocols?: string | Array<string>,
): SocketType {
  const socket = make(url, protocols);
  socket.binaryType = "arraybuffer";
  return socket;
}

export const WebSocketLive = Layer.succeed(WebSocketConstructor)((url, protocols) =>
  makeWebSocketWithArrayBufferFrames(
    (socketUrl, socketProtocols) =>
      socketProtocols === undefined
        ? new globalThis.WebSocket(socketUrl)
        : new globalThis.WebSocket(socketUrl, socketProtocols),
    url,
    protocols,
  ),
);

export const openWebSocket = Effect.fn("WebSocket.open")(function* (
  url: string,
  options?: Parameters<typeof makeWebSocket>[1],
) {
  return yield* makeWebSocket(url, {
    ...options,
    closeCodeIsError: options?.closeCodeIsError ?? ((code) => code !== 1000),
  });
});
