import { describe, expect, it } from "vite-plus/test";

import { harnessForAgentKey, launchTargetMark } from "./agentHarness";

describe("harnessForAgentKey", () => {
  it("matches built-in agent keys to the harness sharing their leading segment", () => {
    expect(harnessForAgentKey("claude")).toBe("claude-code");
    expect(harnessForAgentKey("codex")).toBe("codex");
    expect(harnessForAgentKey("gemini")).toBe("gemini");
    expect(harnessForAgentKey("opencode")).toBe("opencode");
    expect(harnessForAgentKey("pi")).toBe("pi");
  });

  it("matches configured keys that extend a harness id", () => {
    expect(harnessForAgentKey("claude-code")).toBe("claude-code");
    expect(harnessForAgentKey("claude-fast")).toBe("claude-code");
    expect(harnessForAgentKey("codex_review")).toBe("codex");
    expect(harnessForAgentKey("Cursor Agent")).toBe("cursor");
    expect(harnessForAgentKey("vscode")).toBe("vscode-copilot");
  });

  it("only matches whole segments", () => {
    expect(harnessForAgentKey("pixel")).toBeNull();
    expect(harnessForAgentKey("aider")).toBeNull();
    expect(harnessForAgentKey("")).toBeNull();
    expect(harnessForAgentKey("---")).toBeNull();
  });
});

describe("launchTargetMark", () => {
  it("ignores shells and unmatched agents", () => {
    expect(launchTargetMark({ kind: "plain_shell", key: "plain_shell", label: "Plain shell" })).toBeNull();
    expect(launchTargetMark({ kind: "shell", key: "shell", label: "Shell" })).toBeNull();
    expect(launchTargetMark({ kind: "agent", key: "aider", label: "aider" })).toBeNull();
  });

  it("hides the label when the harness name already covers it", () => {
    expect(launchTargetMark({ kind: "agent", key: "claude", label: "Claude" })).toEqual({
      harness: "claude-code",
      showLabel: false,
    });
    expect(launchTargetMark({ kind: "agent", key: "opencode", label: "opencode" })).toEqual({
      harness: "opencode",
      showLabel: false,
    });
    expect(launchTargetMark({ kind: "agent", key: "codex", label: "  " })).toEqual({
      harness: "codex",
      showLabel: false,
    });
  });

  it("keeps a label that says more than the harness name", () => {
    expect(launchTargetMark({ kind: "agent", key: "codex-review", label: "Review Agent" })).toEqual({
      harness: "codex",
      showLabel: true,
    });
    expect(launchTargetMark({ kind: "agent", key: "claude", label: "Claude fast" })).toEqual({
      harness: "claude-code",
      showLabel: true,
    });
  });
});
