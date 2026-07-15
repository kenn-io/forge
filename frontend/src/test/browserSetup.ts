import { afterAll } from "vitest";
import { cdp } from "vitest/browser";

interface GarbageCollectingCDPSession {
  send(method: "HeapProfiler.collectGarbage"): Promise<void>;
}

afterAll(async () => {
  const session = cdp() as GarbageCollectingCDPSession;
  await session.send("HeapProfiler.collectGarbage");
});
