<script lang="ts">
  import { Chip } from "@middleman/ui";

  type ChipSize = "sm" | "md";

  interface ChipVariant {
    name: string;
    className: string;
  }

  const sizes: ChipSize[] = ["sm", "md"];
  const variants: ChipVariant[] = [
    { name: "Green", className: "chip--green" },
    { name: "Red", className: "chip--red" },
    { name: "Amber", className: "chip--amber" },
    { name: "Purple", className: "chip--purple" },
    { name: "Teal", className: "chip--teal" },
    { name: "Muted", className: "chip--muted" },
    { name: "Open", className: "chip--open" },
    { name: "Closed", className: "chip--closed" },
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
        Validation surface for shared chip geometry, tone variants, casing, and
        interactive states.
      </p>
    </header>

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
              {#each variants as variant (variant.className)}
                <Chip size={size} class={variant.className}>{variant.name}</Chip>
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
            <Chip class="chip--green">Uppercase</Chip>
            <Chip class="chip--green" uppercase={false}>plain case</Chip>
          </div>
        </article>

        <article class="behavior-card">
          <h3>Interactivity</h3>
          <p>Button mode keeps shared geometry and hover behavior.</p>
          <div class="chip-row">
            <Chip class="chip--purple" interactive onclick={() => {}}>
              Interactive
            </Chip>
            <Chip class="chip--muted" interactive disabled onclick={() => {}}>
              Disabled
            </Chip>
          </div>
        </article>

        <article class="behavior-card">
          <h3>Compact metadata</h3>
          <p>Typical small-chip usage from list and detail surfaces.</p>
          <div class="chip-row">
            <Chip size="sm" class="chip--muted">+120/-12</Chip>
            <Chip size="sm" class="chip--muted" uppercase={false} dataTestid="descender-chip">kenn-io/msgvault</Chip>
            <Chip size="sm" class="chip--teal">Worktree</Chip>
            <Chip size="sm" class="chip--amber">Draft</Chip>
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
    gap: 20px;
  }

  .hero {
    display: grid;
    gap: 10px;
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
    gap: 18px;
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
    gap: 14px;
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
    gap: 10px;
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
