import { Clipboard } from "@effect/platform-browser";
import { Layer } from "effect";
import { GeneratedApiLive } from "../api/generated-api.js";
import { EventSourceFactoryLive } from "../browser/event-source.js";
import { BrowserObserversLive } from "../browser/observers.js";
import { LocalStorageLive, SessionStorageLive } from "../browser/storage.js";
import { StreamingFetchLive } from "../browser/streaming-fetch.js";
import { WebSocketLive } from "../browser/web-socket.js";

export const AppLiveLayer = Layer.mergeAll(
  GeneratedApiLive,
  EventSourceFactoryLive,
  BrowserObserversLive,
  LocalStorageLive,
  SessionStorageLive,
  StreamingFetchLive,
  WebSocketLive,
  Clipboard.layer,
);
