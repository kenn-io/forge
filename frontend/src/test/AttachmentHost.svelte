<script lang="ts">
  import type { Attachment } from "svelte/attachments";

  interface Props {
    attachment: Attachment<HTMLElement>;
  }

  let { attachment }: Props = $props();
</script>

<!--
  Generic test-only host for exercising a real svelte/attachments
  Attachment's mount/cleanup lifecycle through Svelte's own effect
  machinery (rather than invoking the attachment function by hand),
  since {@attach ...} is template-only syntax unavailable in plain
  .ts test modules.
-->
<div data-testid="attachment-host" {@attach attachment}></div>
