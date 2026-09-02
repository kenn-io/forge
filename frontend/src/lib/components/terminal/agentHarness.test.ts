import { describe, expect, it } from "vite-plus/test";

import { harnessForAgentKey, launchTargetHarness } from "./agentHarness";

describe("harnessForAgentKey", () => {
  it("matches built-in agent keys to a glyph by brand id or agent product name", () => {
    expect(harnessForAgentKey("claude")).toBe("claude");
    expect(harnessForAgentKey("codex")).toBe("openai");
    expect(harnessForAgentKey("gemini")).toBe("gemini");
    expect(harnessForAgentKey("opencode")).toBe("opencode");
    expect(harnessForAgentKey("pi")).toBe("pi");
    expect(harnessForAgentKey("aider")).toBe("aider");
  });

  it("matches configured keys that extend a name", () => {
    expect(harnessForAgentKey("claude-code")).toBe("claude");
    expect(harnessForAgentKey("claude-fast")).toBe("claude");
    expect(harnessForAgentKey("codex_review")).toBe("openai");
    expect(harnessForAgentKey("Cursor Agent")).toBe("cursor");
    expect(harnessForAgentKey("cortex-code")).toBe("snowflake");
  });

  it("falls back to a bare string prefix for names long enough to be unambiguous", () => {
    expect(harnessForAgentKey("claudex")).toBe("claude");
    expect(harnessForAgentKey("codexy-fast")).toBe("openai");
    expect(harnessForAgentKey("geminiwrap")).toBe("gemini");
  });

  it("never lets a short name swallow an unrelated key", () => {
    expect(harnessForAgentKey("pixel")).toBeNull();
    expect(harnessForAgentKey("zebra")).toBeNull();
    expect(harnessForAgentKey("")).toBeNull();
    expect(harnessForAgentKey("---")).toBeNull();
  });
});

describe("launchTargetHarness", () => {
  it("ignores shells and unmatched agents", () => {
    expect(launchTargetHarness({ kind: "plain_shell", key: "plain_shell" })).toBeNull();
    expect(launchTargetHarness({ kind: "shell", key: "shell" })).toBeNull();
    expect(launchTargetHarness({ kind: "agent", key: "totally-unknown" })).toBeNull();
  });

  it("resolves agent keys", () => {
    expect(launchTargetHarness({ kind: "agent", key: "claude" })).toBe("claude");
    expect(launchTargetHarness({ kind: "agent", key: "codex-review" })).toBe("openai");
  });
});
