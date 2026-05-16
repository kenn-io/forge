<script lang="ts">
  import DiffScopePicker from "../diff/DiffScopePicker.svelte";
  import DiffToolbar from "../diff/DiffToolbar.svelte";
  import DiffView from "../diff/DiffView.svelte";
  import { getStores } from "../../context.js";
  import type { WorkspaceDiffBase } from "../../stores/diff.svelte.js";

  interface Props {
    workspaceID: string;
    provider: string;
    platformHost?: string | undefined;
    repoOwner: string;
    repoName: string;
    repoPath: string;
    itemNumber: number;
    active?: boolean;
  }

  const {
    workspaceID,
    provider,
    platformHost,
    repoOwner,
    repoName,
    repoPath,
    itemNumber,
    active = false,
  }: Props = $props();
  const { diff } = getStores();

  let base = $state<WorkspaceDiffBase>("head");
  let loadedKey = "";

  $effect(() => {
    if (!active) return;
    const key = `${workspaceID}:${base}`;
    if (loadedKey === key) return;
    loadedKey = key;
    void diff.loadWorkspaceDiff(workspaceID, base);
  });

  function selectBase(nextBase: WorkspaceDiffBase): void {
    base = nextBase;
  }
</script>

<section class="workspace-diff" aria-label="Workspace Diff">
  <div class="workspace-diff-scope">
    <span class="scope-label">Compare</span>
    <div class="scope-toggle" role="group" aria-label="Workspace diff base">
      <button
        class="scope-btn"
        class:scope-btn--active={base === "head"}
        aria-pressed={base === "head"}
        aria-label="Compare with HEAD"
        title="HEAD"
        onclick={() => selectBase("head")}
      >
        HEAD
      </button>
      <button
        class="scope-btn scope-btn--wide"
        class:scope-btn--active={base === "pushed"}
        aria-pressed={base === "pushed"}
        aria-label="Compare with pushed branch"
        title="Pushed branch"
        onclick={() => selectBase("pushed")}
      >
        Branch
      </button>
      <button
        class="scope-btn scope-btn--wide"
        class:scope-btn--active={base === "merge-target"}
        aria-pressed={base === "merge-target"}
        aria-label="Compare with merge target"
        title="Merge target"
        onclick={() => selectBase("merge-target")}
      >
        Target
      </button>
    </div>
    <DiffScopePicker compact />
  </div>
  <DiffToolbar
    compact
    showRichPreview={false}
    showFileJump
    showScopePicker={false}
  />
  <div class="workspace-diff-layout">
    <div class="workspace-diff-main">
      <DiffView
        {provider}
        {platformHost}
        owner={repoOwner}
        name={repoName}
        {repoPath}
        number={itemNumber}
        loadOnMount={false}
        keyboardActive={false}
        richPreviewEnabled={false}
        reviewMode="disabled"
      />
    </div>
  </div>
</section>

<style>
  .workspace-diff {
    display: flex;
    flex-direction: column;
    container-type: inline-size;
    height: 100%;
    min-width: 0;
    overflow: hidden;
    background: var(--diff-bg);
  }

  .workspace-diff-scope {
    position: relative;
    z-index: 70;
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 32px;
    padding: 5px 12px;
    border-bottom: 1px solid var(--diff-border);
    background: var(--bg-surface);
    flex-shrink: 0;
  }

  .workspace-diff-scope :global(.diff-scope-picker) {
    margin-left: auto;
  }

  .scope-label {
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }

  .scope-toggle {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    width: 184px;
    flex: 0 1 184px;
    min-width: 164px;
    padding: 2px;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    background: var(--bg-inset);
  }

  .scope-btn {
    min-width: 0;
    height: 22px;
    padding: 0 6px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--text-muted);
    font-size: var(--font-size-xs);
    font-weight: 600;
    cursor: pointer;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .scope-btn:hover {
    color: var(--text-primary);
  }

  .scope-btn--active {
    background: var(--accent-blue);
    color: #fff;
  }

  .scope-btn--active:hover {
    color: #fff;
  }

  .workspace-diff-layout {
    display: flex;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .workspace-diff-main {
    display: flex;
    flex: 1;
    min-width: 0;
    overflow: hidden;
  }

</style>
