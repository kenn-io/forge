<script lang="ts">
  import { Chip } from "@kenn-io/kit-ui";

  import {
    hasMergeQueuePosition,
    mergeQueueEntryDescription,
    mergeQueueRemovalDescription,
    mergeQueueState,
    type MergeQueueEntry,
    type MergeQueueRemoval,
  } from "./merge-queue.js";

  interface Props {
    entry?: MergeQueueEntry | undefined;
    removal?: MergeQueueRemoval | undefined;
  }

  const { entry, removal }: Props = $props();

  // The state badge already reads Queued, as the provider's header does, so
  // this chip adds only the position. The entry's queue state stays in the
  // tooltip: the provider's header does not show it, and a queue that rejects
  // an entry removes it, which the removal chip below shows.
  const entryTitle = $derived.by(() => {
    if (!entry) return "";
    const description = mergeQueueEntryDescription(entry);
    if (entry.state.trim().toLowerCase() === "queued") return description;
    return `Queue state: ${mergeQueueState(entry.state).label}. ${description}`;
  });
  const removalTitle = $derived(removal ? mergeQueueRemovalDescription(removal) : undefined);
</script>

{#if entry}
  {#if hasMergeQueuePosition(entry)}
    <Chip size="sm" tone="muted" class="merge-queue-chip" title={entryTitle}>
      #{entry.position} in queue
    </Chip>
  {/if}
{:else if removal}
  <Chip size="sm" tone="danger" class="merge-queue-chip" title={removalTitle}>
    Removed from queue
  </Chip>
{/if}
