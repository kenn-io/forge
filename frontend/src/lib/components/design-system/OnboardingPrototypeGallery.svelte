<script lang="ts">
  import { Tabs } from "@kenn-io/kit-ui";
  import OnboardingChecklistPrototype from "./OnboardingChecklistPrototype.svelte";
  import OnboardingGuidePrototype from "./OnboardingGuidePrototype.svelte";
  import OnboardingWizardPrototype from "./OnboardingWizardPrototype.svelte";

  type PrototypeId = "wizard" | "checklist" | "guide";

  interface PrototypeDefinition {
    id: PrototypeId;
    label: string;
    eyebrow: string;
    idea: string;
    strength: string;
    tradeoff: string;
  }

  const prototypes: Record<PrototypeId, PrototypeDefinition> = {
    wizard: {
      id: "wizard",
      label: "Focused setup",
      eyebrow: "A · Linear path",
      idea: "A temporary setup surface that ends at the first launchable PR workspace.",
      strength: "Fastest path to activation",
      tradeoff: "Hides the wider app until setup is complete",
    },
    checklist: {
      id: "checklist",
      label: "Activation checklist",
      eyebrow: "B · In context",
      idea: "A resumable checklist that teaches setup inside the real application shell.",
      strength: "Users learn the product while configuring it",
      tradeoff: "More interface to parse on first launch",
    },
    guide: {
      id: "guide",
      label: "Start guide",
      eyebrow: "C · Durable help",
      idea: "Task-oriented documentation in the app, paired with live status and previews.",
      strength: "Stays useful after onboarding",
      tradeoff: "Easiest path to postpone or abandon",
    },
  };
  const prototypeOrder: PrototypeId[] = ["wizard", "checklist", "guide"];

  let active = $state<PrototypeId>("wizard");
  const activePrototype = $derived(prototypes[active]);
  const galleryTabs = prototypeOrder.map((id) => ({ id, label: prototypes[id].label }));
</script>

<div class="prototype-gallery">
  <Tabs
    tabs={galleryTabs}
    value={active}
    ariaLabel="Onboarding prototype"
    onchange={(id) => { active = prototypeOrder.find((prototypeId) => prototypeId === id) ?? active; }}
  >
    {#snippet tab(item)}
      <span class="gallery-tab">
        <span>{prototypes[item.id as PrototypeId].eyebrow}</span>
        <strong>{item.label}</strong>
      </span>
    {/snippet}
    {#snippet children()}
      <div class="gallery-panel">
        <div class="comparison-note" aria-live="polite">
          <p>{activePrototype.idea}</p>
          <dl>
            <div><dt>Strength</dt><dd>{activePrototype.strength}</dd></div>
            <div><dt>Trade-off</dt><dd>{activePrototype.tradeoff}</dd></div>
          </dl>
        </div>

        <div class="browser-frame">
          <div class="browser-chrome" aria-hidden="true">
            <div class="traffic-lights"><span></span><span></span><span></span></div>
            <div class="address">127.0.0.1:8091/{active === "guide" ? "start" : active === "checklist" ? "pulls" : "setup"}</div>
            <div class="chrome-spacer"></div>
          </div>
          <div class="prototype-panel">
            {#if active === "wizard"}
              <OnboardingWizardPrototype />
            {:else if active === "checklist"}
              <OnboardingChecklistPrototype />
            {:else}
              <OnboardingGuidePrototype />
            {/if}
          </div>
        </div>
      </div>
    {/snippet}
  </Tabs>
</div>

<style>
  .gallery-panel {
    display: grid;
    gap: var(--space-5);
  }

  .gallery-tab {
    display: grid;
    gap: var(--space-2);
    text-align: left;
  }

  .gallery-tab span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    letter-spacing: var(--letter-spacing-label, 0.07em);
    text-transform: var(--label-transform, uppercase);
  }

  .gallery-tab strong {
    font-size: var(--font-size-md);
  }

  .comparison-note {
    display: grid;
    grid-template-columns: minmax(0, 1.25fr) minmax(360px, 1fr);
    gap: var(--space-6);
    align-items: start;
  }

  .comparison-note > p {
    max-width: 64ch;
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-md);
    line-height: 1.55;
  }

  .comparison-note dl {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-5);
    margin: 0;
  }

  .comparison-note dl > div {
    display: grid;
    gap: var(--space-2);
  }

  .comparison-note dt {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    letter-spacing: var(--letter-spacing-label, 0.06em);
    text-transform: var(--label-transform, uppercase);
  }

  .comparison-note dd {
    margin: 0;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    line-height: 1.4;
  }

  .browser-frame {
    min-width: 0;
    overflow: hidden;
    border: 1px solid var(--border-default);
    border-radius: var(--radius-lg);
    background: var(--bg-primary);
    box-shadow: var(--shadow-md);
  }

  .browser-chrome {
    min-height: 34px;
    display: grid;
    grid-template-columns: 90px minmax(200px, 420px) 90px;
    align-items: center;
    justify-content: center;
    padding: 0 var(--space-4);
    border-bottom: 1px solid var(--border-muted);
    background: var(--bg-inset);
  }

  .traffic-lights {
    display: flex;
    gap: var(--space-3);
  }

  .traffic-lights span {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--border-strong);
  }

  .address {
    overflow: hidden;
    padding: var(--space-2) var(--space-4);
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-sm);
    background: var(--bg-surface);
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-xs);
    text-align: center;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .prototype-panel {
    min-width: 0;
    overflow: hidden;
  }

  @media (max-width: 760px) {
    .comparison-note {
      grid-template-columns: 1fr;
      gap: var(--space-4);
    }

    .comparison-note dl {
      grid-template-columns: 1fr;
      gap: var(--space-4);
    }

    .browser-chrome {
      grid-template-columns: 72px minmax(0, 1fr) 0;
    }
  }
</style>
