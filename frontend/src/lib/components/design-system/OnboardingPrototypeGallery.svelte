<script lang="ts">
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
</script>

<div class="prototype-gallery">
  <div class="gallery-nav" role="tablist" aria-label="Onboarding prototype">
    {#each prototypeOrder as prototypeId (prototypeId)}
      {@const prototype = prototypes[prototypeId]}
      <button
        type="button"
        role="tab"
        aria-label={prototype.label}
        id={`prototype-tab-${prototype.id}`}
        aria-controls={`prototype-panel-${prototype.id}`}
        aria-selected={active === prototype.id}
        tabindex={active === prototype.id ? 0 : -1}
        onclick={() => { active = prototype.id; }}
      >
        <span>{prototype.eyebrow}</span>
        <strong>{prototype.label}</strong>
      </button>
    {/each}
  </div>

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
    <div
      class="prototype-panel"
      role="tabpanel"
      id={`prototype-panel-${active}`}
      aria-labelledby={`prototype-tab-${active}`}
    >
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

<style>
  .prototype-gallery {
    display: grid;
    gap: var(--space-5);
  }

  .gallery-nav {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    border-bottom: 1px solid var(--border-default);
  }

  .gallery-nav button {
    position: relative;
    display: grid;
    gap: var(--space-2);
    padding: var(--space-4) var(--space-5) var(--space-5);
    border: 0;
    background: transparent;
    color: var(--text-muted);
    text-align: left;
    cursor: pointer;
    font: inherit;
  }

  .gallery-nav button::after {
    position: absolute;
    right: 0;
    bottom: -1px;
    left: 0;
    height: 2px;
    background: transparent;
    content: "";
  }

  .gallery-nav button[aria-selected="true"] {
    color: var(--text-primary);
  }

  .gallery-nav button[aria-selected="true"]::after {
    background: var(--accent-blue);
  }

  .gallery-nav button:focus-visible {
    z-index: 1;
    outline: 2px solid var(--accent-blue);
    outline-offset: -2px;
  }

  .gallery-nav span {
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 700;
    letter-spacing: 0.07em;
    text-transform: uppercase;
  }

  .gallery-nav strong {
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
    letter-spacing: 0.06em;
    text-transform: uppercase;
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
    .gallery-nav {
      grid-template-columns: 1fr;
      border: 1px solid var(--border-default);
      border-radius: var(--radius-md);
    }

    .gallery-nav button {
      padding: var(--space-4);
      border-bottom: 1px solid var(--border-muted);
    }

    .gallery-nav button:last-child {
      border-bottom: 0;
    }

    .gallery-nav button::after {
      top: 0;
      right: auto;
      bottom: 0;
      width: 2px;
      height: auto;
    }

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
