<script lang="ts">
  import LockIcon from "@lucide/svelte/icons/lock";

  interface Props {
    // Undefined means the provider has not reported the repository's privacy.
    isPrivate: boolean | undefined;
  }

  const { isPrivate }: Props = $props();
</script>

{#if isPrivate === true}
  <span class="repo-private-icon" role="img" aria-label="Private repository" title="Private repository">
    <LockIcon size={11} strokeWidth={2} aria-hidden="true" />
  </span>
{/if}

<style>
  .repo-private-icon {
    display: inline-flex;
    flex: none;
    align-items: center;
    align-self: center;
    margin-inline-start: 3px;
    vertical-align: -1px;
    color: var(--text-muted);
  }
</style>
