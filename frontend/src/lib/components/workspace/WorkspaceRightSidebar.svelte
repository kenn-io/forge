<script lang="ts">
  import WorkspaceTargets from "./WorkspaceTargets.svelte";
  import { repositoryKeyFromWire } from "../../api/repository-key.js";
  import type { WorkspaceTarget } from "../../api/generated/models/workspaceTarget.js";
  import { EmptyState } from "@kenn-io/kit-ui";
  import type { NumberedRouteItemRef } from "../../routes.js";
  import PullDetail from "../detail/PullDetail.svelte";
  import IssueDetail from "../detail/IssueDetail.svelte";
  import WorkspaceDiffPanel from "./WorkspaceDiffPanel.svelte";
  import WorkspaceReviewsPanel from "./WorkspaceReviewsPanel.svelte";
  import KataLinksPanel from "../kata/KataLinksPanel.svelte";
  import { defaultWorkspaceDiffBase, type WorkspaceDiffGitState } from "./workspace-diff-default.js";
  import type { RepositoryKey } from "../../api/repository-key.js";

  interface Props {
    activeTab: "diff" | "pr" | "issue" | "reviews" | "kata";
    workspaceID: string;
    worktreePath: string;
    workspaceHostKey?: string | undefined;
    provider: string;
    platformHost?: string | undefined;
    repositoryKey?: RepositoryKey | undefined;
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
    targetsRefreshToken?: number;
    diffRefreshToken?: number;
    disabled?: boolean;
    visible?: boolean;
    gitState?: WorkspaceDiffGitState;
    onselect?: (kind: "pr" | "issue", item: NumberedRouteItemRef) => void;
    onselectkata?: () => void;
  }

  let {
    activeTab,
    workspaceID,
    worktreePath,
    workspaceHostKey = undefined,
    provider,
    platformHost,
    repositoryKey,
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
    targetsRefreshToken = 0,
    diffRefreshToken = 0,
    disabled = false,
    visible = true,
    gitState = {},
    onselect,
    onselectkata,
  }: Props = $props();

  let selectedKata = $state<{ daemonID: string; issueUID: string } | undefined>();
  function selectTarget(target: WorkspaceTarget): void {
    if (target.type === "kata" && target.kata) {
      selectedKata = { daemonID: target.kata.daemon_id, issueUID: target.kata.issue_uid };
      onselectkata?.();
    } else if ((target.type === "pr" || target.type === "issue") && target.repo) {
      const repo = target.repo;
      onselect?.(target.type, { provider: repo.provider, platformHost: repo.platform_host, repositoryKey: repositoryKeyFromWire(repo), owner: repo.owner, name: repo.name, repoPath: repo.repo_path, number: target.number });
    }
  }

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
  const workspaceRepo = $derived({ provider, platformHost, repositoryKey, owner: repoOwner, name: repoName, repoPath });
  const displayedPR = $derived(viewedPR ?? (hasPR && associatedPRNumber !== null ? { ...workspaceRepo, number: associatedPRNumber } : null));
  const displayedIssue = $derived(viewedIssue ?? (hasIssue ? { ...workspaceRepo, number: ownerItemNumber } : null));
  const hasMergeTarget = $derived(
    ownerItemType === "pull_request"
      ? ownerItemNumber > 0 && hasRepo
      : hasPR
  );
  const defaultDiffBase = $derived(defaultWorkspaceDiffBase(gitState, hasMergeTarget));

</script>

<div class="right-sidebar-content">
  {#if onselect}
    {#key `${workspaceHostKey ?? "self"}:${workspaceID}`}
      <WorkspaceTargets {workspaceID} {workspaceHostKey} refreshToken={targetsRefreshToken + refreshToken} {disabled} onselect={selectTarget} />
    {/key}
  {/if}
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
        defaultBase={defaultDiffBase}
      />
    {/key}
  {:else if activeTab === "pr"}
    {#if displayedPR}
      {#key `pr:${workspaceHostKey ?? "self"}:${workspaceID}:${refreshToken}`}
        <div class="pr-scroll" inert={disabled}>
          <PullDetail
            {...displayedPR}
            hideTabs={true}
            hideWorkspaceAction={true}
            hideStaleWhileLoading={true}
            onStackMemberNavigate={(ref) => {
              if (!onselect) return false;
              onselect("pr", { ...displayedPR, ...ref });
              return true;
            }}
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
          <IssueDetail {...displayedIssue} hideStaleWhileLoading={true} />
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
        selection={selectedKata}
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
