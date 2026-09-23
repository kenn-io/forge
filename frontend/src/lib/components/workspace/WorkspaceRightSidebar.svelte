<script lang="ts">
  import { EmptyState } from "@kenn-io/kit-ui";
  import type { NumberedRouteItemRef } from "../../routes.js";
  import PullDetail from "../detail/PullDetail.svelte";
  import IssueDetail from "../detail/IssueDetail.svelte";
  import WorkspaceDiffPanel from "./WorkspaceDiffPanel.svelte";
  import WorkspaceReviewsPanel from "./WorkspaceReviewsPanel.svelte";
  import KataLinksPanel from "../kata/KataLinksPanel.svelte";

  interface Props {
    activeTab: "diff" | "pr" | "issue" | "reviews" | "kata";
    workspaceID: string;
    worktreePath: string;
    workspaceHostKey?: string | undefined;
    provider: string;
    platformHost?: string | undefined;
    platformRepoId?: string | undefined;
    repoOwner: string;
    repoName: string;
    repoPath: string;
    ownerItemType: "pull_request" | "issue" | "kata_task" | "adhoc";
    ownerItemNumber: number;
    associatedPRNumber: number | null;
    viewedPR?: NumberedRouteItemRef | null;
    viewedIssue?: NumberedRouteItemRef | null;
    branch: string;
    roborevBaseUrl: string;
    refreshToken?: number;
    diffRefreshToken?: number;
    disabled?: boolean;
    visible?: boolean;
  }

  let {
    activeTab,
    workspaceID,
    worktreePath,
    workspaceHostKey = undefined,
    provider,
    platformHost,
    platformRepoId,
    repoOwner,
    repoName,
    repoPath,
    ownerItemType,
    ownerItemNumber,
    associatedPRNumber,
    viewedPR = null,
    viewedIssue = null,
    branch,
    roborevBaseUrl,
    refreshToken = 0,
    diffRefreshToken = 0,
    disabled = false,
    visible = true,
  }: Props = $props();

  // Determine if we have valid context
  const hasRepo = $derived(
    repoOwner !== "" && repoName !== "",
  );
  const hasPR = $derived(
    associatedPRNumber !== null &&
    associatedPRNumber > 0 &&
    hasRepo
  );
  const hasIssue = $derived(
    ownerItemType === "issue" &&
    ownerItemNumber > 0 &&
    hasRepo
  );
  const workspaceRepo = $derived({ provider, platformHost, platformRepoId, owner: repoOwner, name: repoName, repoPath });
  const displayedPR = $derived(viewedPR ?? (hasPR && associatedPRNumber !== null ? { ...workspaceRepo, number: associatedPRNumber } : null));
  const displayedIssue = $derived(viewedIssue ?? (hasIssue ? { ...workspaceRepo, number: ownerItemNumber } : null));
  const hasMergeTarget = $derived(
    ownerItemType === "pull_request"
      ? ownerItemNumber > 0 && hasRepo
      : hasPR
  );

</script>

<div class="right-sidebar-content">
  {#if activeTab === "diff"}
    {#key `diff:${workspaceHostKey ?? "self"}:${workspaceID}`}
      <WorkspaceDiffPanel
        {workspaceID}
        {workspaceHostKey}
        {provider}
        {platformHost}
        {repoOwner}
        {repoName}
        {repoPath}
        itemNumber={ownerItemNumber}
        active={activeTab === "diff"}
        {refreshToken}
        {diffRefreshToken}
        {disabled}
        showMergeTarget={hasMergeTarget}
      />
    {/key}
  {:else if activeTab === "pr"}
    {#if displayedPR}
      {#key `pr:${workspaceHostKey ?? "self"}:${workspaceID}:${JSON.stringify(displayedPR)}:${refreshToken}`}
        <div class="pr-scroll" inert={disabled}>
          <PullDetail
            {...displayedPR}
            hideTabs={true}
            hideWorkspaceAction={true}
          />
        </div>
      {/key}
    {:else}
      <EmptyState title="No linked PR" />
    {/if}
  {:else if activeTab === "issue"}
    {#if displayedIssue}
      {#key `issue:${workspaceHostKey ?? "self"}:${workspaceID}:${JSON.stringify(displayedIssue)}:${refreshToken}`}
        <div class="pr-scroll" inert={disabled}>
          <IssueDetail {...displayedIssue} />
        </div>
      {/key}
    {:else}
      <EmptyState title="No linked issue" />
    {/if}
  {:else if activeTab === "kata"}
    {#if workspaceHostKey}
      <EmptyState title="Kata links are unavailable for remote workspaces" />
    {:else}
      <KataLinksPanel
        subject={{ kind: "workspace", workspaceID }}
        active={activeTab === "kata"}
        {disabled}
      />
    {/if}
  {:else if activeTab === "reviews" && visible}
    {#if workspaceHostKey}
      <EmptyState title="Local reviews are unavailable for remote workspaces" />
    {:else}
      {#key `${workspaceID}:${worktreePath}:${branch}`}
        <WorkspaceReviewsPanel {workspaceID} {worktreePath} {branch} {roborevBaseUrl} {disabled} />
      {/key}
    {/if}
  {/if}
</div>

<style>
  .right-sidebar-content {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
    background: var(--bg-surface);
  }

  .pr-scroll {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

</style>
