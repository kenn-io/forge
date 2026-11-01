import { describe, expect, it, vi } from "vite-plus/test";
import { page } from "vite-plus/test/browser";
import { render } from "vitest-browser-svelte";

import "../../../app.css";
import type { LaunchTarget } from "../../api/types.js";
import QuickActionSettings from "./QuickActionSettings.svelte";
import SettingsRuntimeHarness from "./SettingsRuntimeHarness.svelte";

describe("QuickActionSettings summary rows (browser)", () => {
  it("keeps the action label visible beside a long agent name on a phone-width viewport", async () => {
    await page.viewport(390, 800);
    const agent: LaunchTarget = {
      key: "custom",
      label: "a-custom-agent-with-a-very-long-configured-display-name",
      kind: "agent",
      source: "config",
      command: ["custom"],
      available: true,
      disabled_reason: "",
    };
    await render(SettingsRuntimeHarness, {
      props: {
        component: QuickActionSettings,
        componentProps: {
          quickActions: [{ label: "triage", agent: "custom", prompt: "triage this" }],
          launchTargets: [agent],
          onUpdate: vi.fn(),
        },
      },
    });

    const summary = page.getByRole("button", { name: /^triage\b/ });
    await expect.element(summary).toBeVisible();
    const row = summary.element();
    const label = row.querySelector<HTMLElement>(".summary-label")!;
    const agentName = row.querySelector<HTMLElement>(".summary-agent-name")!;

    expect(label.getBoundingClientRect().width).toBeGreaterThanOrEqual(label.scrollWidth);
    expect(agentName.scrollWidth).toBeGreaterThan(agentName.clientWidth);
  });
});
