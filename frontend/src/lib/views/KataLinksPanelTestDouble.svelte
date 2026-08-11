<script lang="ts">
  import type { KataLinksSubject } from "../stores/kata-links.svelte.js";

  interface Props {
    subject: KataLinksSubject;
    active: boolean;
    disabled?: boolean;
  }

  let { subject, active, disabled = false }: Props = $props();
</script>

<div
  data-testid="kata-links-panel"
  data-active={String(active)}
  data-disabled={String(disabled)}
  data-subject={JSON.stringify(subject)}
></div>
