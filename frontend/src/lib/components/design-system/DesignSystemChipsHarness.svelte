<!--
  Browser-tier test harness. Renders the same Chip primitives, sizes, tones,
  and casing/interactivity variants that DesignSystemPage.svelte puts on the
  /design-system route, scoped under the same [data-size] row wrappers the
  Playwright e2e selected by. It exists only so a *.browser.svelte.ts spec can
  mount the shipped kit-ui Chip directly (no app shell, no stores) and read
  its real computed styles. The geometry/typography under test comes entirely
  from kit-ui's Chip CSS plus the kit-ui design tokens; this harness adds only
  the matrix row scaffolding the page uses, never chip styling.
-->
<script lang="ts">
  import { Chip } from "@kenn-io/kit-ui";
</script>

<div class="harness">
  <div class="matrix-row" data-size="xs">
    <Chip size="xs" tone="success">Green</Chip>
    <Chip size="xs" tone="muted">Muted</Chip>
  </div>

  <div class="matrix-row" data-size="sm">
    <Chip size="sm" tone="success">Green</Chip>
    <Chip size="sm" tone="muted">Muted</Chip>
  </div>

  <div class="behavior-row">
    <Chip size="sm" tone="success">Uppercase</Chip>
    <Chip size="sm" tone="success" uppercase={false}>plain case</Chip>
    <Chip size="sm" tone="merged" interactive onclick={() => {}}>Interactive</Chip>
    <Chip size="xs" tone="muted" uppercase={false} dataTestid="descender-chip">team/inbox-view</Chip>
  </div>
</div>

<style>
  .harness {
    display: grid;
    gap: 12px;
    padding: 16px;
  }

  .matrix-row,
  .behavior-row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-4);
    align-items: start;
  }
</style>
