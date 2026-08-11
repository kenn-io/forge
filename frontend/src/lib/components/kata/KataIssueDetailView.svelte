<script lang="ts">
  import {
    IssueDetail,
    projectIssueDetail,
    type KataIssueDetailWire,
  } from "@kenn-io/kata-ui/packages/kata-ui/src/index.ts";

  interface Props {
    detail: Record<string, unknown>;
  }

  let { detail }: Props = $props();
  const projected = $derived(projectIssueDetail(detail as unknown as KataIssueDetailWire));
</script>

<IssueDetail detail={projected} actions={[]} />
