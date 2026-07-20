<script module lang="ts">
  export type MergeWarningEntry = {
    kind: "conflict" | "blocked" | "behind" | "required-checks" | "server";
    text: string;
  };
</script>

<script lang="ts">
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import GitMergeIcon from "@lucide/svelte/icons/git-merge";
  import { Chip } from "@kenn-io/kit-ui";

  interface Props {
    warnings: MergeWarningEntry[];
    pullURL: string;
    providerLabel: string;
    expanded?: boolean;
    ontoggle?: ((expanded: boolean) => void) | undefined;
  }

  let {
    warnings,
    pullURL,
    providerLabel,
    expanded = $bindable(false),
    ontoggle,
  }: Props = $props();

  const hasConflict = $derived(warnings.some((warning) => warning.kind === "conflict"));
  const countLabel = $derived(
    `${warnings.length} merge warning${warnings.length === 1 ? "" : "s"}`,
  );
  const chipLabel = $derived(hasConflict ? "Conflicts" : countLabel);
  const chipAriaLabel = $derived(hasConflict ? `Merge conflicts, ${countLabel}` : countLabel);

  function toggleExpanded(): void {
    const next = !expanded;
    expanded = next;
    ontoggle?.(next);
  }
</script>

{#if warnings.length > 0}
  <div class="merge-warnings-status">
    <Chip size="sm"
      interactive={true}
      tone={hasConflict ? "warning" : "neutral"}
      uppercase={false}
      ariaLabel={chipAriaLabel}
      dataTestid="merge-warnings-chip"
      onclick={toggleExpanded}
      title={expanded ? "Collapse merge warnings" : "Expand merge warnings"}
      {expanded}
    >
      <GitMergeIcon size={12} strokeWidth={2.3} aria-hidden="true" />
      <span>{chipLabel}</span>
      {#snippet trailing()}
        <ChevronDownIcon
          class={["chip-chevron", expanded && "chip-chevron--open"].filter(Boolean).join(" ")}
          size={12}
          strokeWidth={2.4}
          aria-hidden="true"
        />
      {/snippet}
    </Chip>

    {#if expanded}
      <div class="merge-warnings-collapse">
        <div class="merge-warnings-panel" aria-label="Merge warnings">
          {#each warnings as warning, index (`${index}-${warning.kind}`)}
            <div
              class="merge-warning-line"
              class:merge-warning-line--conflict={warning.kind === "conflict"}
            >
              <span>{warning.text}</span>
            </div>
          {/each}
          <a
            class="merge-warnings-link"
            href={pullURL}
            target="_blank"
            rel="noopener noreferrer"
          >View on {providerLabel}</a>
        </div>
      </div>
    {/if}
  </div>
{/if}

<style>
  .merge-warnings-status {
    display: contents;
  }

  :global(.chip-chevron) {
    flex-shrink: 0;
    vertical-align: middle;
    transition: transform 0.15s;
  }

  :global(.chip-chevron--open) {
    transform: rotate(180deg);
  }

  /* Full-width row in the wrapping chips-row: order pushes the panel
   * below every chip, matching StackStatus's collapse behavior. */
  .merge-warnings-collapse {
    order: 999;
    flex-basis: 100%;
    width: 100%;
    min-width: 0;
    margin-top: 4px;
  }

  .merge-warnings-panel {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px 12px;
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--accent-blue) 10%, transparent);
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .merge-warning-line--conflict {
    color: var(--accent-amber);
  }

  .merge-warnings-link {
    color: inherit;
    text-decoration: underline;
    align-self: flex-start;
  }
</style>
