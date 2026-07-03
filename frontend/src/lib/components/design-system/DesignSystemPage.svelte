<script lang="ts">
  import { Chip } from "@middleman/ui";
  import type { ChipSize, ChipTone } from "@kenn-io/kit-ui";
  import DesignSystemTabbedPanelDemo from "./DesignSystemTabbedPanelDemo.svelte";
  import DesignSystemTypeaheadDemo from "./DesignSystemTypeaheadDemo.svelte";

  interface ChipVariant {
    name: string;
    tone: ChipTone;
  }

  const sizes: ChipSize[] = ["xs", "sm", "md"];
  const variants: ChipVariant[] = [
    { name: "Success", tone: "success" },
    { name: "Danger", tone: "danger" },
    { name: "Warning", tone: "warning" },
    { name: "Merged", tone: "merged" },
    { name: "Info", tone: "info" },
    { name: "Workspace", tone: "workspace" },
    { name: "Muted", tone: "muted" },
    { name: "Neutral", tone: "neutral" },
    { name: "Canceled", tone: "canceled" },
  ];
</script>

<svelte:head>
  <title>middleman design system</title>
</svelte:head>

<div class="design-system-page">
  <div class="page-shell">
    <header class="hero">
      <p class="eyebrow">Shared primitives</p>
      <h1>Design system</h1>
      <p class="intro">
        Validation surface for shared primitives used across maintainer
        workflows.
      </p>
    </header>

    <section class="card" aria-labelledby="tabbed-panel-title">
      <div class="section-header">
        <div>
          <p class="section-kicker">TabbedPanelTree</p>
          <h2 id="tabbed-panel-title">Tabbed workspace panels</h2>
        </div>
        <p class="section-copy">
          Neutral panel workspace with draggable tabs, split drops, and
          resizable panes.
        </p>
      </div>

      <DesignSystemTabbedPanelDemo />
    </section>

    <section class="card" aria-labelledby="typeahead-title">
      <div class="section-header">
        <div>
          <p class="section-kicker">RepoTypeahead</p>
          <h2 id="typeahead-title">Typeahead dropdown states</h2>
        </div>
        <p class="section-copy">
          Repository picker states using the configured repo tree and shared
          dropdown row treatment.
        </p>
      </div>

      <DesignSystemTypeaheadDemo />
    </section>

    <section class="card" aria-labelledby="chip-variants-title">
      <div class="section-header">
        <div>
          <p class="section-kicker">Chip</p>
          <h2 id="chip-variants-title">Size and tone matrix</h2>
        </div>
        <p class="section-copy">
          Confirms size modifiers and externally supplied color classes survive
          build output and render with shared geometry.
        </p>
      </div>

      <div class="matrix" role="table" aria-label="Chip variant matrix">
        <div class="matrix-head" role="row">
          <div class="matrix-label matrix-label--head" role="columnheader">
            Size
          </div>
          <div class="matrix-chips" role="columnheader">
            Variants
          </div>
        </div>

        {#each sizes as size (size)}
          <div class="matrix-row" role="row" data-size={size}>
            <div class="matrix-label" role="rowheader">{size.toUpperCase()}</div>
            <div class="matrix-chips">
              {#each variants as variant (variant.tone)}
                <Chip size={size} tone={variant.tone}>{variant.name}</Chip>
              {/each}
            </div>
          </div>
        {/each}
      </div>
    </section>

    <section class="card grid" aria-labelledby="chip-behavior-title">
      <div class="section-header section-header--stacked">
        <div>
          <p class="section-kicker">Behavior</p>
          <h2 id="chip-behavior-title">Casing and interaction</h2>
        </div>
      </div>

      <div class="behavior-grid">
        <article class="behavior-card">
          <h3>Text casing</h3>
          <p>Default uppercase and opt-out plain-case rendering.</p>
          <div class="chip-row">
            <Chip size="sm" tone="success">Uppercase</Chip>
            <Chip size="sm" tone="success" uppercase={false}>plain case</Chip>
          </div>
        </article>

        <article class="behavior-card">
          <h3>Interactivity</h3>
          <p>Button mode keeps shared geometry and hover behavior.</p>
          <div class="chip-row">
            <Chip size="sm" tone="merged" interactive onclick={() => {}}>
              Interactive
            </Chip>
            <Chip size="sm" tone="muted" interactive disabled onclick={() => {}}>
              Disabled
            </Chip>
          </div>
        </article>

        <article class="behavior-card">
          <h3>Compact metadata</h3>
          <p>Typical small-chip usage from list and detail surfaces.</p>
          <div class="chip-row">
            <Chip size="xs" tone="muted">+120/-12</Chip>
            <Chip size="xs" tone="muted" uppercase={false} dataTestid="descender-chip">team/inbox-view</Chip>
            <Chip size="xs" tone="workspace">Worktree</Chip>
            <Chip size="xs" tone="warning">Draft</Chip>
          </div>
        </article>
      </div>
    </section>

    <section class="card grid" aria-labelledby="inbox-pattern-title">
      <div class="section-header section-header--stacked">
        <div>
          <p class="section-kicker">Inbox</p>
          <h2 id="inbox-pattern-title">Notification row states</h2>
        </div>
        <p class="section-copy">
          Reference for Inbox rows: reason chips, linked PR metadata, external-only
          subjects, and queued GitHub read propagation status.
        </p>
      </div>

      <div class="inbox-examples" aria-label="Inbox notification examples">
        <article class="inbox-row inbox-row--unread">
          <div class="inbox-row-main">
            <div class="inbox-title">Review requested on acme/widget#42</div>
            <div class="inbox-meta">
              <Chip size="xs" tone="merged">Review requested</Chip>
              <Chip size="xs" tone="muted">PR #42</Chip>
              <span>acme/widget</span>
            </div>
          </div>
          <button class="demo-button" type="button">Done</button>
        </article>

        <article class="inbox-row">
          <div class="inbox-row-main">
            <div class="inbox-title">Discussion announcement</div>
            <div class="inbox-meta">
              <Chip size="xs" tone="workspace">Mention</Chip>
              <Chip size="xs" tone="muted">External</Chip>
              <span>opens on GitHub</span>
            </div>
          </div>
          <button class="demo-button" type="button">Open</button>
        </article>

        <article class="inbox-row">
          <div class="inbox-row-main">
            <div class="inbox-title">Queued read propagation</div>
            <div class="inbox-meta">
              <Chip size="xs" tone="warning">Queued</Chip>
              <span>GitHub read update will sync in background</span>
            </div>
          </div>
        </article>
      </div>
    </section>
  </div>
</div>

<style>
  .design-system-page {
    flex: 1;
    overflow: auto;
    background:
      radial-gradient(circle at top left, color-mix(in srgb, var(--accent-blue) 12%, transparent), transparent 28%),
      var(--bg-primary);
  }

  .page-shell {
    max-width: 1120px;
    margin: 0 auto;
    padding: 32px 24px 48px;
    display: grid;
    gap: var(--space-6);
  }

  .hero {
    display: grid;
    gap: var(--space-4);
  }

  .eyebrow,
  .section-kicker {
    margin: 0;
    color: var(--accent-blue);
    font-size: var(--font-size-sm);
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  h1,
  h2,
  h3,
  p {
    margin: 0;
  }

  h1 {
    color: var(--text-primary);
    font-size: var(--font-size-xl);
    line-height: 1.1;
  }

  .intro,
  .section-copy,
  .behavior-card p {
    color: var(--text-secondary);
    font-size: var(--font-size-lg);
    line-height: 1.5;
    max-width: 70ch;
  }

  .card {
    background: var(--bg-surface);
    border: 1px solid var(--border-default);
    border-radius: 16px;
    box-shadow: var(--shadow-sm);
    padding: 20px;
    display: grid;
    gap: var(--space-6);
  }

  .grid {
    gap: 16px;
  }

  .section-header {
    display: flex;
    justify-content: space-between;
    gap: 16px;
    align-items: end;
    flex-wrap: wrap;
  }

  .section-header--stacked {
    align-items: start;
  }

  h2 {
    color: var(--text-primary);
    font-size: var(--font-size-xl);
    line-height: 1.2;
    margin-top: 4px;
  }

  .matrix {
    display: grid;
    gap: 12px;
  }

  .matrix-head,
  .matrix-row {
    display: grid;
    grid-template-columns: 72px minmax(0, 1fr);
    gap: var(--space-5);
    align-items: start;
  }

  .matrix-label {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    font-weight: 700;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    padding-top: 4px;
  }

  .matrix-label--head {
    color: var(--text-secondary);
  }

  .matrix-chips,
  .chip-row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-4);
  }

  .behavior-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 16px;
  }

  .behavior-card {
    border: 1px solid var(--border-muted);
    border-radius: 14px;
    background: var(--bg-inset);
    padding: 16px;
    display: grid;
    gap: 12px;
  }

  .inbox-examples {
    display: grid;
    gap: var(--space-4);
  }

  .inbox-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 16px;
    border: 1px solid var(--border-muted);
    border-radius: 14px;
    background: var(--bg-inset);
    padding: 14px 16px;
  }

  .inbox-row--unread {
    border-color: color-mix(in srgb, var(--accent-blue) 42%, var(--border-muted));
    box-shadow: inset 3px 0 0 var(--accent-blue);
  }

  .inbox-row-main {
    display: grid;
    gap: 8px;
    min-width: 0;
  }

  .inbox-title {
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    font-weight: 650;
  }

  .inbox-meta {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .demo-button {
    border: 1px solid var(--border-default);
    border-radius: 8px;
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    font-weight: 650;
    padding: 6px 10px;
  }

  h3 {
    color: var(--text-primary);
    font-size: var(--font-size-lg);
    line-height: 1.3;
  }

  @media (max-width: 900px) {
    .behavior-grid {
      grid-template-columns: 1fr;
    }
  }

  @media (max-width: 640px) {
    .page-shell {
      padding-inline: 16px;
      padding-top: 24px;
    }

    .matrix-head,
    .matrix-row {
      grid-template-columns: 1fr;
      gap: 8px;
    }

    .matrix-label {
      padding-top: 0;
    }
  }
</style>
