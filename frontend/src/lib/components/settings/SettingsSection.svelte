<script lang="ts">
  import type { Snippet } from "svelte";

  interface Props {
    title: string;
    sectionId?: string;
    children: Snippet;
  }

  let { title, sectionId, children }: Props = $props();
</script>

<section class="settings-section" id={sectionId}>
  <h2 class="section-title">{title}</h2>
  <div class="section-body">
    {@render children()}
  </div>
</section>

<style>
  .settings-section {
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    background: var(--bg-surface);
  }

  .section-title {
    font-size: var(--font-size-md);
    font-weight: 600;
    color: var(--text-primary);
    padding: 12px 16px;
    border-bottom: 1px solid var(--border-muted);
  }

  .section-body {
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
</style>
