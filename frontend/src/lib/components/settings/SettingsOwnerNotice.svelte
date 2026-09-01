<script lang="ts">
  import { settingsOwnerName, type SettingsOwner } from "./settingsOwnership.js";

  interface Props {
    owner: SettingsOwner;
    subject: string;
  }

  let { owner, subject }: Props = $props();
</script>

<p class="owner-notice" data-owner={owner}>{subject} is managed by {settingsOwnerName(owner)}.</p>

<style>
  .owner-notice {
    margin: 0 0 var(--space-3);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }
</style>
