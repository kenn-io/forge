<script lang="ts">
  import { copyToClipboard } from "@kenn-io/kit-ui";
  // ToolingStatusBlock renders the embedding host's view of git and
  // provider CLI availability/authentication. It is consumed by the
  // First Run Panel (gates provider-dependent actions) and the New
  // Worktree sheet (gates the PR/issue source radios). The block itself
  // is informational - it never blocks a flow, it just tells the user
  // what is missing and how to recover.

  import type { ToolingStatusValue } from "../../stores/embed-config.svelte.ts";

  interface Props {
    tooling: ToolingStatusValue | undefined;
    provider?: string | undefined;
    // When the embedder cannot detect tooling state at all (the
    // /api/tooling endpoint failed, or the host has not pushed yet),
    // hide the block entirely. The parent decides whether to fall
    // back to a global "tooling unavailable" notice.
    hideWhenUnknown?: boolean;
  }

  let { tooling, provider, hideWhenUnknown = false }: Props = $props();

  let copied = $state<string | null>(null);

  type ToolStatus = "ok" | "missing" | "unauthed" | "unknown";
  type ProviderToolName = "gh" | "glab";
  type ProviderTooling = NonNullable<ToolingStatusValue[ProviderToolName]>;

  const providerToolName = $derived<ProviderToolName>(
    provider?.toLowerCase() === "gitlab" ? "glab" : "gh",
  );
  const providerTool = $derived<ProviderTooling | undefined>(
    tooling?.[providerToolName],
  );
  const installCommand = $derived(`brew install ${providerToolName}`);
  const authCommand = $derived(`${providerToolName} auth login`);

  const gitStatus = $derived<ToolStatus>(
    tooling?.git
      ? (tooling.git.available ? "ok" : "missing")
      : "unknown",
  );

  const providerCliStatus = $derived<ToolStatus>(
    providerTool
      ? (!providerTool.available
          ? "missing"
          : (providerTool.authenticated ? "ok" : "unauthed"))
      : "unknown",
  );

  const gitDetail = $derived.by(() => {
    if (!tooling?.git) return "";
    if (tooling.git.available) {
      return tooling.git.version
        ? `Available (${tooling.git.version})`
        : "Available";
    }
    return "Not found on PATH";
  });

  const providerCliDetail = $derived.by(() => {
    if (!providerTool) return "";
    if (!providerTool.available) return "Not installed";
    if (!providerTool.authenticated) return "Not authenticated";
    const userPart = providerTool.user ? `as ${providerTool.user}` : "";
    const hostPart = providerTool.host ? `on ${providerTool.host}` : "";
    const tail = [userPart, hostPart].filter(Boolean).join(" ");
    return tail ? `Authenticated ${tail}` : "Authenticated";
  });

  const showBlock = $derived(
    !hideWhenUnknown ||
      gitStatus !== "unknown" ||
      providerCliStatus !== "unknown",
  );

  async function copyCommand(command: string): Promise<void> {
    if (!(await copyToClipboard(command))) {
      console.error("Copy failed");
      return;
    }
    copied = command;
    setTimeout(() => {
      if (copied === command) copied = null;
    }, 1500);
  }
</script>

{#if showBlock}
  <section class="tooling-block" aria-label="Tooling status">
    <h3 class="tooling-block__title">Tooling</h3>
    <ul class="tooling-block__list">
      <li class="tooling-row" data-status={gitStatus}>
        <div class="tooling-row__main">
          <span
            class="tooling-row__indicator"
            data-status={gitStatus}
            aria-hidden="true"
          ></span>
          <span class="tooling-row__name">git</span>
          <span class="tooling-row__detail">{gitDetail}</span>
        </div>
        {#if gitStatus === "missing"}
          <p class="tooling-row__recovery">
            Install Xcode Command Line Tools:
            <code>xcode-select --install</code>
          </p>
        {/if}
      </li>

      <li class="tooling-row" data-status={providerCliStatus}>
        <div class="tooling-row__main">
          <span
            class="tooling-row__indicator"
            data-status={providerCliStatus}
            aria-hidden="true"
          ></span>
          <span class="tooling-row__name">{providerToolName}</span>
          <span class="tooling-row__detail">{providerCliDetail}</span>
        </div>
        {#if providerCliStatus === "missing"}
          <p class="tooling-row__recovery">
            Install with:
            <code>{installCommand}</code>
            <button
              type="button"
              class="tooling-row__copy"
              onclick={() => copyCommand(installCommand)}
              aria-label={providerToolName === "gh"
                ? "Copy install command"
                : `Copy ${providerToolName} install command`}
            >
              {copied === installCommand ? "Copied" : "Copy"}
            </button>
          </p>
        {/if}
        {#if providerCliStatus === "unauthed"}
          <p class="tooling-row__recovery">
            Authenticate with:
            <code>{authCommand}</code>
            <button
              type="button"
              class="tooling-row__copy"
              onclick={() => copyCommand(authCommand)}
              aria-label={providerToolName === "gh"
                ? "Copy auth command"
                : `Copy ${providerToolName} auth command`}
            >
              {copied === authCommand ? "Copied" : "Copy"}
            </button>
          </p>
        {/if}
      </li>
    </ul>
  </section>
{/if}

<style>
  .tooling-block {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px;
    border: 1px solid var(--border-muted);
    border-radius: var(--radius-md, 8px);
    background: var(--bg-surface);
  }

  .tooling-block__title {
    margin: 0;
    font-size: var(--font-size-xs);
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
  }

  .tooling-block__list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .tooling-row {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .tooling-row__main {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: var(--font-size-md);
  }

  .tooling-row__indicator {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  }

  .tooling-row__indicator[data-status="ok"] {
    background: var(--accent-green);
  }

  .tooling-row__indicator[data-status="missing"] {
    background: var(--accent-red);
  }

  .tooling-row__indicator[data-status="unauthed"] {
    background: var(--accent-amber);
  }

  .tooling-row__indicator[data-status="unknown"] {
    background: var(--text-muted);
  }

  .tooling-row__name {
    font-family: var(--font-mono, monospace);
    font-weight: 600;
  }

  .tooling-row__detail {
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
  }

  .tooling-row__recovery {
    margin: 0;
    padding-left: 16px;
    color: var(--text-secondary);
    font-size: var(--font-size-sm);
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
  }

  .tooling-row__recovery code {
    padding: 2px 5px;
    border-radius: 4px;
    background: var(--bg-inset);
    color: var(--text-primary);
    font-family: var(--font-mono, monospace);
    font-size: var(--font-size-xs);
  }

  .tooling-row__copy {
    padding: 2px 6px;
    border: 1px solid var(--border-muted);
    border-radius: 4px;
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
  }
</style>
