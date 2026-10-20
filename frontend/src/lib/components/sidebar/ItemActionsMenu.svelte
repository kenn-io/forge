<script lang="ts">
  import {
    copyToClipboard,
    Menu,
    MenuContent,
    MenuItem,
    MenuRadioGroup,
    MenuRadioItem,
    MenuSeparator,
    type MenuAnchorPoint,
  } from "@kenn-io/kit-ui";
  import CheckCircleIcon from "@lucide/svelte/icons/circle-check";
  import CircleDotIcon from "@lucide/svelte/icons/circle-dot";
  import CircleXIcon from "@lucide/svelte/icons/circle-x";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import FilePenIcon from "@lucide/svelte/icons/file-pen";
  import LinkIcon from "@lucide/svelte/icons/link";
  import StarIcon from "@lucide/svelte/icons/star";
  import TagIcon from "@lucide/svelte/icons/tag";
  import UsersIcon from "@lucide/svelte/icons/users";
  import ZapIcon from "@lucide/svelte/icons/zap";
  import { Effect } from "effect";
  import type { KanbanStatus, Label, QuickAction } from "../../api/types.js";
  import { executeGeneratedApiRequest } from "../../api/generated-api.js";
  import { providerHostRouteParams, providerRouteParams, providerUsesHostRoute } from "../../api/provider-routes.js";
  import type { AppExecution } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import { getStores } from "../../context.js";
  import { runQuickActionOnItems } from "../../stores/bulk-quick-action.js";
  import { loadLabelCatalogWithRefresh } from "../detail/labelCatalogRefresh.js";
  import { showFlash } from "../../stores/flash.svelte.js";
  import {
    actionEligibility,
    listItemLabel,
    listItemRepoKey,
    listTargetsNoun,
    runListItemAction,
    type ListItemAction,
    type ListItemTarget,
  } from "../../stores/item-actions.js";
  import { refreshRepoOperations, repoOperations } from "../../stores/repo-operations.svelte.js";
  import {
    quickActionDisabledReason,
    quickActionTarget,
    sortQuickActionsByLabel,
  } from "../../stores/workspace-quick-actions.js";
  import ConfirmDialog from "../shared/ConfirmDialog.svelte";
  import BulkMembershipDialog, { type MembershipOption } from "./BulkMembershipDialog.svelte";

  // Right-click menu for PR and issue rows. Every action applies to the
  // right-clicked item, or to every selected item when it is part of a
  // multi-row selection. Items an action does not apply to are skipped and
  // the menu says how many it will affect.
  interface Props {
    open: boolean;
    anchor: MenuAnchorPoint | undefined;
    returnFocus: HTMLElement | undefined;
    targets: readonly ListItemTarget[];
    selectionActive: boolean;
    onClearSelection: () => void;
    /** Reload the list (and an open detail) after items changed. */
    onChanged: () => void;
  }

  let {
    open = $bindable(),
    anchor,
    returnFocus,
    targets,
    selectionActive,
    onClearSelection,
    onChanged,
  }: Props = $props();

  const runtime = getAppRuntime();
  const { settings } = getStores();

  const kanbanOptions: { value: KanbanStatus; label: string }[] = [
    { value: "new", label: "New" },
    { value: "reviewing", label: "Reviewing" },
    { value: "waiting", label: "Waiting" },
    { value: "awaiting_merge", label: "Awaiting merge" },
  ];

  type StateAction = "close" | "reopen" | "draft" | "ready";
  const stateActionVerbs: Record<StateAction, string> = {
    close: "Close",
    reopen: "Reopen",
    draft: "Convert to draft",
    ready: "Mark ready for review",
  };

  const kind = $derived(targets[0]?.kind ?? "pull");
  const noun = (count: number) => listTargetsNoun(targets, count);
  const count = $derived(targets.length);
  const multi = $derived(count > 1);
  const quickActions = $derived(sortQuickActionsByLabel(settings.getQuickActions()));
  const launchTargets = $derived(settings.getLaunchTargets());

  let running = $state(false);
  let confirmQuickAction = $state.raw<{ action: QuickAction; targets: readonly ListItemTarget[] } | null>(null);
  let confirmState = $state.raw<{ action: StateAction; targets: readonly ListItemTarget[] } | null>(null);

  // Load each repository's mutation availability when the menu opens, so
  // actions blocked by credentials or rate limits show why.
  $effect(() => {
    if (!open) return;
    const repos = new Map(targets.map((target) => [listItemRepoKey(target), target.item.repo]));
    refreshRepoOperations(runtime, [...repos.values()]);
  });

  function plan(action: ListItemAction["type"]) {
    const verdicts = targets.map((target) => ({
      target,
      verdict: actionEligibility(target, action, repoOperations(target.item.repo)),
    }));
    const eligible = verdicts.flatMap(({ target, verdict }) => (verdict.eligible ? [target] : []));
    const offered = verdicts.some(({ verdict }) => verdict.eligible || !verdict.hidden);
    const blocked = verdicts.find(({ verdict }) => !verdict.eligible && !verdict.hidden)?.verdict;
    const reason = blocked && !blocked.eligible ? blocked.reason : "";
    return { eligible, offered, reason };
  }

  function countLabel(verb: string, eligibleCount: number): string {
    if (!multi) return `${verb} ${noun(1)}`;
    if (eligibleCount === count || eligibleCount === 0) return `${verb} ${count} ${noun(count)}`;
    return `${verb} ${eligibleCount} of ${count} ${noun(count)}`;
  }

  function finish(): void {
    running = false;
    onChanged();
  }

  function run(items: readonly ListItemTarget[], action: ListItemAction | ((target: ListItemTarget) => ListItemAction)): void {
    if (items.length === 0) return;
    running = true;
    runListItemAction(runtime, items, action, { onSettled: finish });
  }

  function chooseStateAction(action: StateAction, items: readonly ListItemTarget[]): void {
    if (items.length > 1) confirmState = { action, targets: items };
    else run(items, { type: action });
  }

  function confirmStateAction(): void {
    const pending = confirmState;
    if (!pending) return;
    running = true;
    runListItemAction(runtime, pending.targets, { type: pending.action }, {
      onSettled: () => {
        confirmState = null;
        finish();
        onClearSelection();
      },
    });
  }

  function itemList(items: readonly ListItemTarget[]): string {
    const labels = items.slice(0, 3).map(listItemLabel);
    const rest = items.length - labels.length;
    return `${labels.join(", ")}${rest > 0 ? ` and ${rest} more` : ""}`;
  }

  function copyLinks(): void {
    const text = targets.map((target) => target.item.URL).join("\n");
    runtime.runCommand(
      Effect.promise(() => copyToClipboard(text)).pipe(
        Effect.tap((copied) =>
          Effect.sync(() => {
            if (copied) showFlash(multi ? `Copied ${count} links.` : "Copied link.", { tone: "success" });
            else showFlash("Could not copy to the clipboard.", { tone: "danger" });
          }),
        ),
      ),
      { operation: "copy item links", safeContext: { count }, onFailure: () => {} },
    );
  }

  function openInBrowser(): void {
    const url = targets[0]?.item.URL;
    if (url) window.open(url, "_blank", "noopener,noreferrer");
  }

  const allStarred = $derived(targets.every((target) => target.item.Starred));
  const commonPullKanban = $derived.by(() => {
    const statuses = new Set(targets.flatMap((target) => (target.kind === "pull" ? [target.item.KanbanStatus] : [])));
    return statuses.size === 1 ? [...statuses][0] ?? "" : "";
  });

  // Quick actions

  function chooseQuickAction(action: QuickAction): void {
    const items = [...targets];
    if (items.length === 1) runQuick(action, items);
    else confirmQuickAction = { action, targets: items };
  }

  // Launches finish in the background, possibly after workspaces finish
  // setting up; one message reports the outcome when they all have.
  function runQuick(action: QuickAction, items: readonly ListItemTarget[]): void {
    confirmQuickAction = null;
    onClearSelection();
    runQuickActionOnItems(runtime, items, action, settings.getWorkspaceSettings().default_execution_target ?? "");
  }

  function agentLabel(action: QuickAction): string {
    return quickActionTarget(action, launchTargets)?.label ?? action.agent;
  }

  // Labels and assignees

  type MembershipEdit = {
    field: "labels" | "assignees";
    targets: readonly ListItemTarget[];
    options: MembershipOption[];
    loading: boolean;
    error: string;
    /** Label names each repository offers; adds skip repositories without the label. */
    catalogs: ReadonlyMap<string, ReadonlySet<string>>;
  };
  let membership = $state.raw<MembershipEdit | null>(null);
  let membershipLoad: Pick<AppExecution<void, unknown>, "interrupt"> | null = null;
  let userSearch: Pick<AppExecution<void, unknown>, "interrupt"> | null = null;

  const membershipCounts = $derived.by(() => {
    const counts = new Map<string, number>();
    if (!membership) return counts;
    for (const target of membership.targets) {
      const names = membership.field === "labels"
        ? (target.item.labels ?? []).map((label) => label.name)
        : (target.item.assignees ?? []);
      for (const name of names) counts.set(name, (counts.get(name) ?? 0) + 1);
    }
    return counts;
  });

  function repoRoute(target: ListItemTarget) {
    const ref = {
      provider: target.item.repo.provider,
      platformHost: target.item.repo.platform_host,
      owner: target.item.repo.owner,
      name: target.item.repo.name,
      repoPath: target.item.repo.repo_path,
    };
    return providerUsesHostRoute(ref)
      ? { onHost: true as const, params: providerHostRouteParams(ref) }
      : { onHost: false as const, params: providerRouteParams(ref) };
  }

  function distinctRepos(items: readonly ListItemTarget[]): ListItemTarget[] {
    return [...new Map(items.map((target) => [listItemRepoKey(target), target])).values()];
  }

  function labelOption(label: Label): MembershipOption {
    return { key: label.name, label: label.name, color: label.color || "", description: label.description };
  }

  function openLabels(items: readonly ListItemTarget[]): void {
    // Labels already on the items show while the catalogs load.
    const current = new Map<string, Label>();
    for (const target of items) for (const label of target.item.labels ?? []) current.set(label.name, label);
    const repos = distinctRepos(items);
    const loaded = new Map<string, Label[]>();
    // Repositories whose catalog is still loading or refreshing; Apply waits for them.
    const refreshing = new Set(repos.map(listItemRepoKey));
    let refreshFailed = false;
    membership = {
      field: "labels",
      targets: items,
      options: [...current.values()].map(labelOption),
      loading: true,
      error: "",
      catalogs: new Map(),
    };
    const publish = (): void => {
      if (membership?.field !== "labels") return;
      const byName = new Map(current);
      for (const labels of loaded.values()) for (const label of labels) if (!byName.has(label.name)) byName.set(label.name, label);
      membership = {
        ...membership,
        loading: refreshing.size > 0,
        error: refreshFailed ? "Could not load repository labels, so labels can only be removed." : "",
        options: [...byName.values()]
          .map(labelOption)
          .sort((a, b) => a.label.localeCompare(b.label, undefined, { sensitivity: "base" })),
        catalogs: new Map([...loaded].map(([repo, labels]) => [repo, new Set(labels.map((label) => label.name))])),
      };
    };
    membershipLoad?.interrupt();
    // A stale catalog refreshes in the background on the server; poll it
    // like the detail label picker until it is current.
    membershipLoad = runtime.runCommand(
      Effect.forEach(
        repos,
        (target) => {
          const route = repoRoute(target);
          const repo = listItemRepoKey(target);
          return loadLabelCatalogWithRefresh({
            isActive: () => membership?.field === "labels",
            loadOnce: executeGeneratedApiRequest("GET repository labels", (client, signal) =>
              route.onHost
                ? client.RepositoriesService.listRepoLabelsOnHost(route.params, { signal })
                : client.RepositoriesService.listRepoLabels(route.params, { signal }),
            ).pipe(
              Effect.map((response) => ({
                labels: response.labels ?? [],
                stale: response.stale ?? false,
                syncing: response.syncing ?? false,
                syncError: response.sync_error ?? "",
              })),
            ),
            onUpdate: (catalog) =>
              Effect.sync(() => {
                const settled = !catalog.stale && !catalog.syncing;
                if (settled) refreshing.delete(repo);
                // A failed refresh still reports the list as current, with
                // the error set; its labels are not trusted for additions.
                if (settled && catalog.syncError) {
                  loaded.delete(repo);
                  refreshFailed = true;
                } else {
                  loaded.set(repo, catalog.labels);
                }
                publish();
              }),
          });
        },
        { concurrency: 4, discard: true },
      ),
      {
        operation: "load label catalogs",
        safeContext: { count: items.length },
        onFailure: () => {
          if (membership?.field === "labels") {
            membership = {
              ...membership,
              loading: false,
              error: "Could not load repository labels, so labels can only be removed.",
            };
          }
        },
      },
    );
  }

  function assigneeOptions(items: readonly ListItemTarget[], found: readonly string[]): MembershipOption[] {
    const names = new Set<string>();
    for (const target of items) for (const login of target.item.assignees ?? []) names.add(login);
    for (const login of found) names.add(login);
    return [...names]
      .sort((a, b) => a.localeCompare(b, undefined, { sensitivity: "base" }))
      .map((login) => ({ key: login, label: login }));
  }

  function openAssignees(items: readonly ListItemTarget[]): void {
    membership = {
      field: "assignees",
      targets: items,
      options: assigneeOptions(items, []),
      loading: false,
      error: "",
      catalogs: new Map(),
    };
    searchUsers("");
  }

  // Candidates come from each repository's mention autocomplete, the same
  // source the detail assignee editor uses.
  function searchUsers(query: string): void {
    const edit = membership;
    if (edit?.field !== "assignees") return;
    userSearch?.interrupt();
    userSearch = runtime.runCommand(
      Effect.sleep(query === "" ? "0 millis" : "250 millis").pipe(
        Effect.andThen(
          Effect.forEach(
            distinctRepos(edit.targets),
            (target) => {
              const route = repoRoute(target);
              const params = { trigger: "@", q: query, limit: 25 };
              return executeGeneratedApiRequest("GET assignee candidates", (client, signal) =>
                route.onHost
                  ? client.RepositoriesService.getCommentAutocompleteOnHost(route.params, params, { signal })
                  : client.RepositoriesService.getCommentAutocomplete(route.params, params, { signal }),
              ).pipe(Effect.map((response) => response.users ?? []));
            },
            { concurrency: 4 },
          ),
        ),
        Effect.flatMap((results) =>
          Effect.sync(() => {
            if (membership?.field !== "assignees") return;
            membership = { ...membership, options: assigneeOptions(membership.targets, results.flat()) };
          }),
        ),
      ),
      { operation: "search assignee candidates", safeContext: {}, onFailure: () => {} },
    );
  }

  function membershipTitle(edit: MembershipEdit): string {
    const [only] = edit.targets;
    const subject = edit.targets.length === 1 && only
      ? listItemLabel(only)
      : `${edit.targets.length} ${listTargetsNoun(edit.targets)}`;
    return `${edit.field === "labels" ? "Labels" : "Assignees"} for ${subject}`;
  }

  function closeMembership(): void {
    membershipLoad?.interrupt();
    userSearch?.interrupt();
    membership = null;
  }

  function applyMembership(change: { add: string[]; remove: string[] }): void {
    const edit = membership;
    if (!edit) return;
    running = true;
    const actionFor = (target: ListItemTarget): ListItemAction => {
      if (edit.field === "assignees") return { type: "assignees", ...change };
      const catalog = edit.catalogs.get(listItemRepoKey(target));
      return {
        type: "labels",
        // A label is only added where the repository has it.
        add: catalog ? change.add.filter((name) => catalog.has(name)) : [],
        remove: change.remove,
      };
    };
    runListItemAction(runtime, edit.targets, actionFor, {
      onSettled: () => {
        closeMembership();
        finish();
      },
    });
  }
</script>

{#snippet stateItem(action: StateAction, Icon: typeof CircleXIcon)}
  {@const p = plan(action)}
  {#if p.offered}
    <MenuItem
      disabled={running || p.eligible.length === 0}
      tone={action === "close" ? "danger" : "neutral"}
      onselect={() => chooseStateAction(action, p.eligible)}
    >
      {#snippet icon()}<Icon size={14} />{/snippet}
      {#snippet trailing()}{p.eligible.length === 0 ? p.reason : ""}{/snippet}
      {countLabel(stateActionVerbs[action], p.eligible.length)}
    </MenuItem>
  {/if}
{/snippet}

<Menu bind:open {anchor} {returnFocus} class="item-actions-menu-root">
  <MenuContent
    ariaLabel={multi ? `Actions for ${count} ${noun(count)}` : `${kind === "pull" ? "Pull request" : "Issue"} actions`}
    class="item-actions-menu"
  >
    {#if !multi}
      <MenuItem onselect={openInBrowser}>
        {#snippet icon()}<ExternalLinkIcon size={14} />{/snippet}
        Open in browser
      </MenuItem>
    {/if}
    <MenuItem onselect={copyLinks}>
      {#snippet icon()}<LinkIcon size={14} />{/snippet}
      {multi ? `Copy ${count} links` : "Copy link"}
    </MenuItem>
    <MenuItem disabled={running} onselect={() => run(targets, { type: "star", starred: !allStarred })}>
      {#snippet icon()}<StarIcon size={14} />{/snippet}
      {allStarred ? "Unstar" : "Star"}{multi ? ` ${count} ${noun(count)}` : ""}
    </MenuItem>

    {@const labels = plan("labels")}
    {@const assignees = plan("assignees")}
    {#if labels.offered || assignees.offered}
      <MenuSeparator />
    {/if}
    {#if labels.offered}
      <MenuItem disabled={running || labels.eligible.length === 0} onselect={() => openLabels(labels.eligible)}>
        {#snippet icon()}<TagIcon size={14} />{/snippet}
        {#snippet trailing()}{labels.eligible.length === 0 ? labels.reason : ""}{/snippet}
        Edit labels...
      </MenuItem>
    {/if}
    {#if assignees.offered}
      <MenuItem disabled={running || assignees.eligible.length === 0} onselect={() => openAssignees(assignees.eligible)}>
        {#snippet icon()}<UsersIcon size={14} />{/snippet}
        {#snippet trailing()}{assignees.eligible.length === 0 ? assignees.reason : ""}{/snippet}
        Edit assignees...
      </MenuItem>
    {/if}

    {@const kanban = plan("kanban")}
    {#if kanban.offered}
      <MenuSeparator />
      <MenuRadioGroup
        ariaLabel={kanban.eligible.length === count ? "Workflow status" : `Workflow status for ${kanban.eligible.length} ${listTargetsNoun(kanban.eligible)}`}
        value={commonPullKanban}
        onchange={(status) => run(kanban.eligible, { type: "kanban", status: status as KanbanStatus })}
      >
        {#each kanbanOptions as option (option.value)}
          <MenuRadioItem value={option.value} disabled={running} closeOnSelect>{option.label}</MenuRadioItem>
        {/each}
      </MenuRadioGroup>
    {/if}

    {@const stateActions = (["ready", "draft", "reopen", "close"] as const).filter((action) => plan(action).offered)}
    {#if stateActions.length > 0}
      <MenuSeparator />
      {@render stateItem("ready", CheckCircleIcon)}
      {@render stateItem("draft", FilePenIcon)}
      {@render stateItem("reopen", CircleDotIcon)}
      {@render stateItem("close", CircleXIcon)}
    {/if}

    {#if quickActions.length > 0}
      <MenuSeparator />
      {#each quickActions as action (action.label)}
        {@const reason = quickActionDisabledReason(action, launchTargets)}
        <MenuItem disabled={reason !== "" || running} textValue={action.label} onselect={() => chooseQuickAction(action)}>
          {#snippet icon()}<ZapIcon size={14} />{/snippet}
          {#snippet trailing()}{reason || agentLabel(action)}{/snippet}
          {multi ? `${action.label} on ${count} ${noun(count)}...` : action.label}
        </MenuItem>
      {/each}
    {/if}

    {#if selectionActive}
      <MenuSeparator />
      <MenuItem onselect={onClearSelection}>Clear selection</MenuItem>
    {/if}
  </MenuContent>
</Menu>

<ConfirmDialog
  open={confirmState !== null}
  title={confirmState
    ? `${stateActionVerbs[confirmState.action]} ${confirmState.targets.length} ${listTargetsNoun(confirmState.targets)}?`
    : ""}
  message={confirmState ? `${stateActionVerbs[confirmState.action]} ${itemList(confirmState.targets)}?` : ""}
  confirmLabel={confirmState
    ? `${stateActionVerbs[confirmState.action]} ${confirmState.targets.length} ${listTargetsNoun(confirmState.targets)}`
    : ""}
  pendingLabel="Updating…"
  busy={running}
  tone={confirmState?.action === "close" ? "danger" : "primary"}
  frameId="item-state-confirm"
  onCancel={() => { confirmState = null; }}
  onConfirm={confirmStateAction}
/>

<ConfirmDialog
  open={confirmQuickAction !== null}
  title={`Run "${confirmQuickAction?.action.label ?? ""}" on ${confirmQuickAction?.targets.length ?? 0} ${listTargetsNoun(confirmQuickAction?.targets ?? [], 2)}?`}
  message={confirmQuickAction ? `Run "${confirmQuickAction.action.label}" on ${itemList(confirmQuickAction.targets)}?` : ""}
  hint={confirmQuickAction
    ? `Each gets a workspace, reusing one that already exists, and ${agentLabel(confirmQuickAction.action)} starts with the action's prompt.`
    : undefined}
  confirmLabel={`Run on ${confirmQuickAction?.targets.length ?? 0} ${listTargetsNoun(confirmQuickAction?.targets ?? [], 2)}`}
  frameId="item-quick-action-confirm"
  onCancel={() => { confirmQuickAction = null; }}
  onConfirm={() => { if (confirmQuickAction) runQuick(confirmQuickAction.action, confirmQuickAction.targets); }}
/>

{#if membership}
  <BulkMembershipDialog
    open
    title={membershipTitle(membership)}
    options={membership.options}
    counts={membershipCounts}
    total={membership.targets.length}
    filterLabel={membership.field === "labels" ? "Filter labels" : "Search people"}
    loading={membership.loading}
    error={membership.error}
    emptyText={membership.field === "labels" ? "No labels found" : "No people found"}
    busy={running}
    allowAdd={membership.field === "assignees" || membership.error === ""}
    frameId="item-membership-edit"
    onfilter={membership.field === "assignees" ? searchUsers : undefined}
    onCancel={closeMembership}
    onApply={applyMembership}
  />
{/if}

<style>
  /* Item menus list every action in one level, so they use the viewport
   * height rather than the kit's short default cap. */
  :global(.kit-menu__content.item-actions-menu) {
    max-height: calc(100vh - 16px);
  }
</style>
