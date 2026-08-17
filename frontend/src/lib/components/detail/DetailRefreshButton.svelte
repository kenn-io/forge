<script lang="ts">
  import RefreshCwIcon from "@lucide/svelte/icons/refresh-cw";
  import { IconButton, Spinner } from "@kenn-io/kit-ui";

  interface Props {
    disabled?: boolean;
    refreshing: boolean;
    onRefresh: () => void;
  }

  let { disabled = false, refreshing, onRefresh }: Props = $props();
</script>

<IconButton
  size="sm"
  ariaLabel="Refresh detail"
  title="Refresh detail"
  disabled={disabled || refreshing}
  onclick={onRefresh}
>
  {#if refreshing}
    <Spinner size={14} label="Refreshing detail" />
  {:else}
    <RefreshCwIcon size={14} strokeWidth={2} aria-hidden="true" />
  {/if}
</IconButton>
