<script lang="ts">
  import { Button } from "@kenn-io/kit-ui";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
  import { showFlash } from "../../stores/flash.svelte.js";
  import { getStores } from "../../context.js";
  import { runApproveWorkflows, type PRDetailActionInput } from "./keyboard-actions.js";

  const { detail } = getStores();

  interface Props {
    owner: string;
    name: string;
    number: number;
    provider: string;
    platformHost?: string | undefined;
    repoPath: string;
    count: number;
    size?: "sm" | "md";
    compactLabel?: boolean;
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
    compactLabel = false,
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
      stores: { detail },
      ...(oncompleted !== undefined && { onCompleted: oncompleted }),
      onError: (message) => showFlash(message, { tone: "danger" }),
      onSettled: () => {
        submitting = false;
      },
    };
  }

  function handleApproveWorkflows(): void {
    if (disabled || submitting) return;
    submitting = true;
    runApproveWorkflows(buildInput());
  }
</script>

<div class="workflow-approval-section">
  <Button
    class="btn btn--workflow-approval"
    onclick={handleApproveWorkflows}
    disabled={submitting || disabled}
    tone="workflow"
    surface="soft"
    title={title ?? tooltip}
    ariaLabel={compactLabel && !submitting ? label : undefined}
    label={submitting ? "Approving workflows…" : compactLabel ? shortLabel : label}
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
