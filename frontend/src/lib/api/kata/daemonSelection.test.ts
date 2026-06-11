import { describe, expect, it } from "vite-plus/test";

import type { KataDaemonInfo } from "./daemons";
import { kataLinkingEnabledForEffectiveDaemon } from "./daemonSelection";

const daemons: KataDaemonInfo[] = [
  {
    id: "home",
    url: "http://127.0.0.1:7777",
    default: true,
    auth: "none",
    health: "down",
  },
  {
    id: "work",
    url: "http://127.0.0.1:8888",
    default: false,
    auth: "none",
    health: "connected",
  },
];

describe("kataLinkingEnabledForEffectiveDaemon", () => {
  it("uses the connected active daemon when one is selected", () => {
    expect(kataLinkingEnabledForEffectiveDaemon(daemons, "work", "home")).toBe(true);
  });

  it("does not enable linking just because another daemon is connected", () => {
    expect(kataLinkingEnabledForEffectiveDaemon(daemons, undefined, "home")).toBe(false);
  });
});
