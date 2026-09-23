<script lang="ts">
  import { EmptyState, Typeahead, type TypeaheadOption } from "@kenn-io/kit-ui";
  import { Effect, Option, Schema } from "effect";
  import { onDestroy, untrack } from "svelte";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import { canonicalProvider, resolvedPlatformHost, type ProviderRouteRef } from "../../api/provider-routes.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import type { AppExecution } from "../../app/runtime.js";
  import PullDetail from "../detail/PullDetail.svelte";

  const { workspaceID, workspaceHostKey, repo, linkedPRNumber, refreshToken, disabled }:
    {
      workspaceID: string;
      workspaceHostKey?: string | undefined;
      repo: ProviderRouteRef;
      linkedPRNumber: number | null;
      refreshToken: number;
      disabled: boolean;
    } = $props();

  const runtime = getAppRuntime();
  const storageKey = $derived(`kenn-forge-workspace-viewed-pr:${JSON.stringify([workspaceHostKey ?? "self", workspaceID])}`);
  const PRNumber = Schema.NumberFromString.check(Schema.isInt(), Schema.isGreaterThan(0));
  let viewedPRNumber = $state<number | null>(untrack(readSelection));
  const displayedPRNumber = $derived(viewedPRNumber ?? linkedPRNumber);
  let options = $state.raw<TypeaheadOption[]>([]);
  let loading = $state(false);
  let error = $state("");
  let searchExecution: AppExecution<unknown, unknown> | undefined;

  function readSelection(): number | null {
    try {
      return Option.getOrNull(Schema.decodeUnknownOption(PRNumber)(localStorage.getItem(storageKey)));
    } catch {
      return null;
    }
  }

  function selectPR(value: string): void {
    viewedPRNumber = value === "" ? null : Number(value);
    try {
      if (viewedPRNumber === null) localStorage.removeItem(storageKey);
      else localStorage.setItem(storageKey, String(viewedPRNumber));
    } catch {
      // Best-effort UI preference persistence; keep the in-memory selection.
    }
  }

  function searchPRs(query: string): void {
    searchExecution?.interrupt();
    loading = true;
    error = "";
    options = [];
    const repoFilter = `${canonicalProvider(repo.provider)}|${resolvedPlatformHost(repo.provider, repo.platformHost)}/${repo.repoPath}`;
    searchExecution = runtime.runCommand(
      Effect.sleep("150 millis").pipe(
        Effect.andThen(executeGeneratedApiRequest("search workspace pull requests", (client, signal) =>
          client.PullRequestsService.listPulls({ repo: repoFilter, state: "all", q: query.trim(), limit: 30 }, { signal }),
        )),
        Effect.tap((pulls) => Effect.sync(() => {
          options = pulls.map((pull) => ({
            name: String(pull.Number),
            label: `#${pull.Number} · ${pull.Title}`,
            meta: pull.State === "merged" ? "Merged" : pull.State === "closed" ? "Closed" : pull.IsDraft ? "Draft" : "Open",
          }));
        })),
        Effect.ensuring(Effect.sync(() => { loading = false; })),
      ),
      {
        operation: "search workspace pull requests",
        safeContext: { workspaceID },
        onFailure: () => { error = "Could not load PRs. Type to retry."; },
      },
    );
  }

  onDestroy(() => searchExecution?.interrupt());
</script>

<div class="pr-picker">
  <Typeahead
    {options}
    value={String(displayedPRNumber ?? "")}
    fallbackLabel={displayedPRNumber ? `#${displayedPRNumber}` : "Choose a PR"}
    placeholder="Search PRs"
    title="Search PRs by number or title"
    emptyLabel="No PRs found"
    {disabled}
    {loading}
    {error}
    remote
    allowClear={viewedPRNumber !== null}
    clearLabel="Use linked PR"
    onquery={searchPRs}
    onselect={selectPR}
    --typeahead-max-width="100%"
  />
</div>
{#if displayedPRNumber && displayedPRNumber > 0}
  {#key `${displayedPRNumber}:${refreshToken}`}
    <div class="pr-scroll" inert={disabled}>
      <PullDetail
        {...repo}
        number={displayedPRNumber}
        hideTabs={true}
        hideWorkspaceAction={true}
      />
    </div>
  {/key}
{:else}
  <EmptyState title="No linked PR" description="Search by number or title to choose a PR." />
{/if}

<style>
  .pr-picker {
    padding: var(--space-3);
    border-bottom: 1px solid var(--border-muted);
  }

  .pr-scroll {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
</style>
