<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { getClient, getStores } from "../../context.js";
    import {
    runApproveWorkflows, type PRDetailActionInput,
  } from "./keyboard-actions.js";

  const client = getClient();
  const { detail, pulls } = getStores();

  interface Props {
    owner: string;
    name: string;
    number: number;
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    count: number;
    size?: "sm" | "md";
    disabled?: boolean;
    /** Tooltip override; pass the unavailable_reason when disabling. */
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
    count,
    size = "md",
    disabled = false,
    title = undefined,
    oncompleted,
  }: Props = $props();

  let submitting = $state(false);

  const label = $derived(
    count > 1 ? `Approve workflows (${count})` : "Approve workflows",
  );
  const shortLabel = $derived(
    count > 1 ? `Workflows (${count})` : "Workflows",
  );
  const tooltip =
    "Approve pending GitHub Actions runs waiting on outside contributor approval";

  function buildInput(): PRDetailActionInput {
    return {
      pr: { State: "open", IsDraft: false, MergeableState: "" },
      ref: { provider, platformHost, owner, name, repoPath },
      number,
      viewerCan: {
        approve: false, merge: false, markReady: false,
        approveWorkflows: true,
      },
      repoSettings: null,
      stale: disabled,
      stores: { detail, pulls },
      client,
      ...(oncompleted !== undefined && { onCompleted: oncompleted }),
    };
  }

  async function handleApproveWorkflows(): Promise<void> {
    if (disabled) return;
    submitting = true;
    try {
      await runApproveWorkflows(buildInput());
    } catch (err) {
      showFlash(err instanceof Error ? err.message : String(err), { tone: "danger" });
    } finally {
      submitting = false;
    }
  }
</script>

<div class="workflow-approval-section">
  <Button
    class="btn btn--workflow-approval"
    onclick={() => void handleApproveWorkflows()}
    disabled={submitting || disabled}
    tone="workflow"
    surface="soft"
    title={title ?? tooltip}
    label={submitting ? "Approving workflows…" : label}
    shortLabel={submitting ? "Approving…" : shortLabel}
    {size}
  >
    <WorkflowIcon size="14" strokeWidth="2.2" aria-hidden="true" />
  </Button>
</div>

<style>
  .workflow-approval-section {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

</style>
