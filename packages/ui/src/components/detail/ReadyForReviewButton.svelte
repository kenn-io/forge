<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import SendHorizontalIcon from "@lucide/svelte/icons/send-horizontal";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { getClient, getStores } from "../../context.js";
    import { runMarkReady, type PRDetailActionInput } from "./keyboard-actions.js";

  const client = getClient();
  const { detail, pulls } = getStores();

  interface Props {
    owner: string;
    name: string;
    number: number;
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    size?: "sm" | "md";
    disabled?: boolean;
    /** Tooltip; pass the unavailable_reason when disabling the action. */
    title?: string | undefined;
    oncompleted?: () => void;
  }

  const {
    owner,
    name,
    number,
    provider,
    platformHost,
    repoPath,
    size = "md",
    disabled = false,
    title = undefined,
    oncompleted,
  }: Props = $props();

  let submitting = $state(false);

  function buildInput(): PRDetailActionInput {
    return {
      pr: { State: "open", IsDraft: true, MergeableState: "" },
      ref: { provider, platformHost, owner, name, repoPath },
      number,
      viewerCan: {
        approve: false, merge: false, markReady: true,
        approveWorkflows: false,
      },
      repoSettings: null,
      stale: disabled,
      stores: { detail, pulls },
      client,
      ...(oncompleted !== undefined && { onCompleted: oncompleted }),
    };
  }

  async function handleReadyForReview(): Promise<void> {
    if (disabled) return;
    submitting = true;
    try {
      await runMarkReady(buildInput());
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
    } finally {
      submitting = false;
    }
  }
</script>

<div class="ready-section">
  <Button
    class="btn btn--ready"
    onclick={() => void handleReadyForReview()}
    disabled={submitting || disabled}
    tone="info"
    surface="soft"
    {title}
    label={submitting ? "Publishing…" : "Ready for review"}
    shortLabel={submitting ? "Publishing…" : "Ready"}
    {size}
  >
    <SendHorizontalIcon size="14" strokeWidth="2.2" aria-hidden="true" />
  </Button>
</div>

<style>
  .ready-section {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

</style>
