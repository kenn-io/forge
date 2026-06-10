<script lang="ts">
  import { untrack } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import { SplitResizeHandle, type SplitResizeEvent } from "@middleman/ui";
  import type {
    MessagesAPI,
    MessagesAggregateRow,
    MessagesCapabilities,
    MessageDetailData,
    MessageSummary,
    MessagesSearchMode,
    MessagesSearchResult,
  } from "../../api/messages/types";
  import type { IssueSummary, IssueRef, KataAPI } from "../../messages/types";
  import type {
    SavedSearchesAPI,
    SavedSearchesAPIError,
  } from "../../api/messages/savedSearchesClient";
  import { buildLinkedMessagesIndex, findIssuesLinkedToMessage } from "../../messages/reverseLinks";
  import type { MessagesRoute } from "../../messages/route";
  import { messageIdFromRoute } from "../../messages/route";
  import type { MessageLinkInput } from "../../messages/messageLinks";
  import MessagesBanner from "./MessagesBanner.svelte";
  import MessagesSearchBar from "./MessagesSearchBar.svelte";
  import MessagesList from "./MessagesList.svelte";
  import MessageDetail from "./MessageDetail.svelte";
  import MessageThread from "./MessageThread.svelte";
  import MessagesFacets from "./MessagesFacets.svelte";
  import MessagesSavedViews from "./MessagesSavedViews.svelte";
  import LinkedMessagesView from "./LinkedMessagesView.svelte";
  import { addFilterToQuery } from "./searchQuery";
  import {
    QUICK_VIEWS,
    addSavedSearch,
    removeSavedSearch,
    type SavedSearch,
  } from "../../messages/savedSearches";

  interface Props {
    messagesApi: MessagesAPI;
    savedSearchesApi: SavedSearchesAPI;
    capabilities: MessagesCapabilities;
    route: MessagesRoute;
    onRouteChange: (next: MessagesRoute) => void;
    /**
     * Optional callback for the banner's "Configure" button. Threaded from
     * App.svelte so the topbar setup dialog can re-open from inside the
     * workspace when capabilities come back degraded. Absent in unit tests
     * and the banner falls back to a text-only alert.
     */
    onConfigure?: (() => void) | undefined;
    /**
     * Resolved kata API for issue search. Optional because Messages-only
     * deployments do not have Kata issue context.
     */
    kata?: Pick<KataAPI, "search"> | undefined;
    /**
     * App-owned callback that resolves the chosen issue, applies the
     * computed metadata patch, and returns the qualified id for the
     * success toast. Hidden when undefined.
     */
    onLinkMessage?: ((
      issueUid: string,
      input: MessageLinkInput,
    ) => Promise<{ qualified_id: string }>) | undefined;
    /**
     * App-owned; jumps to the issue in Tasks mode.
     */
    onOpenIssue?: ((uid: string) => void) | undefined;
    /**
     * Monotonic counter owned by App.svelte; bumped after every successful
     * messagesApi.configure(...) so a same-URL+env reconfigure with rotated
     * credentials still clears per-message UI consent state.
     */
    messagesConfigVersion: number;
  }

  let {
    messagesApi,
    savedSearchesApi,
    capabilities,
    route,
    onRouteChange,
    onConfigure,
    kata,
    onLinkMessage,
    onOpenIssue,
    messagesConfigVersion,
  }: Props = $props();

  // Current search mode - not serialized to the URL in v1b (only one mode
  // is available). Declared here so the $effect below can read it alongside
  // route.q without prop threading.
  // Snapshot-on-mount: derive from capabilities so a "vector"-only deployment
  // starts correctly. untrack avoids the state_referenced_locally warning -
  // we intentionally read the initial value once, not reactively.
  let searchMode = $state<MessagesSearchMode>(untrack(() => capabilities.modes[0] ?? "fts"));

  // Search result state - owned by MessagesWorkspace, passed down to MessagesList.
  let searchResult: MessagesSearchResult | null = $state(null);
  let loadingList = $state(false);
  let searchError: string | null = $state(null);

  // Generation counter - incremented on every $effect run so stale responses
  // from a previous query can be identified and discarded.
  // searchGen tracks in-flight searches; each new run invalidates older ones.
  let searchGen = 0;

  // Fire messagesApi.search() whenever the query or search mode changes.
  // Gated on capabilities.status === "ok": Svelte runs $effects regardless
  // of which template branch renders, so banner-only states (down /
  // unauthorized / misconfigured / absent) must not invoke the backend.
  $effect(() => {
    const q = route.q ?? "";
    const mode = searchMode;
    if (capabilities.status !== "ok") {
      // Invalidate any in-flight fetch from a prior ok state and clear
      // the list - switching capabilities mid-session shouldn't leave
      // stale results visible if/when the workspace mounts again.
      searchGen++;
      searchResult = null;
      searchError = null;
      loadingList = false;
      return;
    }
    const gen = ++searchGen;
    loadingList = true;
    searchError = null;
    void messagesApi
      .search(q, { mode })
      .then((result) => {
        if (gen !== searchGen) return; // stale - discard
        searchResult = result;
      })
      .catch((err: unknown) => {
        if (gen !== searchGen) return; // stale - discard
        searchError = err instanceof Error ? err.message : "Search failed.";
        searchResult = null;
      })
      .finally(() => {
        if (gen !== searchGen) return; // stale - discard
        loadingList = false;
      });
  });

  // Facet sidebar state - three parallel aggregate fetches kept in sync
  // with route.q via a debounced effect. `null` means "loading" (the
  // sidebar shows skeletons); a non-null array means "loaded" (possibly
  // empty). facetsError is sticky once set and cleared on the next run.
  let senders = $state<MessagesAggregateRow[] | null>(null);
  let labels = $state<MessagesAggregateRow[] | null>(null);
  let domains = $state<MessagesAggregateRow[] | null>(null);
  let facetsError = $state<string | null>(null);
  let facetsGen = 0;
  const FACET_DEBOUNCE_MS = 200;

  let facetTimer: ReturnType<typeof setTimeout> | null = null;

  // facetsGen tracks in-flight aggregates; debounce + counter together
  // keep facet rows in sync with the current route.q. Gated on
  // capabilities.status === "ok" because Svelte runs $effects regardless
  // of which template branch renders, so banner states must not fetch.
  //
  // Live cost note: on every route.q change (after a 200ms debounce) this
  // fires three parallel `messagesApi.aggregates(...)` calls - none of which
  // are cached by the Go proxy. (Only /api/v1/msgvault/health caches its body,
  // and only for 5 seconds.) Each render therefore translates to three
  // live aggregate requests to msgvault, so the debounce above is the
  // only thing keeping rapid typing or back-and-forth pagination from
  // saturating the upstream. Keep both the debounce and any pagination
  // throttling in place until the proxy grows its own aggregate cache.
  $effect(() => {
    if (capabilities.status !== "ok") {
      // Mirror the search/detail effects: orphan any in-flight batch from
      // a prior ok state and clear the sidebar payload. The aside isn't
      // rendered in non-ok branches, so this is defense-in-depth - but a
      // template restructure that hoists the sidebar above the gate
      // shouldn't leak stale rows from a previous capability state.
      if (facetTimer) {
        clearTimeout(facetTimer);
        facetTimer = null;
      }
      facetsGen++;
      senders = null;
      labels = null;
      domains = null;
      facetsError = null;
      return;
    }
    const q = route.q ?? "";
    // Clear any pending fetch from a still-typing user.
    if (facetTimer) {
      clearTimeout(facetTimer);
      facetTimer = null;
    }
    // Reset to loading immediately so the sidebar shows skeletons
    // during the debounce window - the previous result set is stale.
    // Clear any prior error too: a fresh fetch shouldn't render the
    // old alert while loading.
    senders = null;
    labels = null;
    domains = null;
    facetsError = null;
    const gen = ++facetsGen;
    facetTimer = setTimeout(() => {
      Promise.all([
        messagesApi.aggregates("senders", { q, limit: 20 }),
        messagesApi.aggregates("labels",  { q, limit: 20 }),
        messagesApi.aggregates("domains", { q, limit: 20 }),
      ])
        .then(([s, l, d]) => {
          if (gen !== facetsGen) return;
          senders = s.rows;
          labels = l.rows;
          domains = d.rows;
          facetsError = null;
        })
        .catch((err: unknown) => {
          if (gen !== facetsGen) return;
          facetsError = err instanceof Error ? err.message : "Failed to load facets.";
          senders = [];
          labels = [];
          domains = [];
        });
    }, FACET_DEBOUNCE_MS);
    return () => {
      if (facetTimer) {
        clearTimeout(facetTimer);
        facetTimer = null;
      }
    };
  });

  // Centralised conversion: route.message (string | null) -> positive integer id or null.
  let selectedID = $derived(messageIdFromRoute(route.message));

  // Detail fetch state - owned here, passed down to MessageDetail.
  let detail: MessageDetailData | null = $state(null);
  let loadingDetail = $state(false);
  let detailError: string | null = $state(null);

  // Generation counter - same race-guard pattern as the search effect.
  // detailGen also orphans in-flight fetches when route.message clears or goes invalid - bump in early-return paths.
  let detailGen = 0;

  // Fetch message detail whenever route.message changes. Gated on
  // capabilities.status === "ok" for the same reason as the search effect:
  // a banner-only state must not call messagesApi.message() because the UI
  // can't render its result anyway and the call is expected to fail.
  $effect(() => {
    const parsed = messageIdFromRoute(route.message);
    if (capabilities.status !== "ok") {
      detailGen++;       // orphan any in-flight fetch from a prior ok state
      detail = null;
      detailError = null;
      loadingDetail = false;
      return;
    }
    if (route.message === null || route.message === "") {
      detailGen++;       // orphan any in-flight fetch
      detail = null;
      detailError = null;
      loadingDetail = false;
      return;
    }
    if (parsed === null) {
      console.warn("ignoring non-integer messages message id:", route.message);
      detailGen++;       // orphan any in-flight fetch
      detail = null;
      detailError = null;
      loadingDetail = false;
      return;
    }
    const gen = ++detailGen;
    loadingDetail = true;
    detailError = null;
    void messagesApi
      .message(parsed)
      .then((d) => {
        if (gen === detailGen) detail = d;
      })
      .catch((err: unknown) => {
        if (gen !== detailGen) return;
        detailError = err instanceof Error ? err.message : "Failed to load message.";
      })
      .finally(() => {
        if (gen === detailGen) loadingDetail = false;
      });
  });

  // ---------- v2a: per-message UI consent + view-mode state ----------

  // loadedImagesFor stores composite keys "${messageID}:${token}" so consent
  // automatically re-prompts when the sanitizer mints a new token for the
  // same message ID (content change, generation rotation, etc.).
  // viewModeFor uses bare message IDs because HTML/text choice is a
  // per-message preference, independent of token.
  const loadedImagesFor = new SvelteSet<string>();
  const viewModeFor = new SvelteMap<number, "html" | "text">();

  // imageConsentKey composes the loadedImagesFor entry for a given
  // message+token. Centralizing this keeps the call sites consistent.
  function imageConsentKey(id: number, token: string): string {
    return `${id}:${token}`;
  }

  let lastIdentity: string | null = null;
  $effect(() => {
    // Fingerprint combines the daemon URL+env (catches swapping backend)
    // with messagesConfigVersion (catches same-URL+env reconfigure that
    // rotated credentials). On transient capability down, lastIdentity
    // is preserved so consent survives daemon hiccups.
    const next = capabilities?.ok
      ? `${capabilities.url}::${capabilities.api_key_env ?? ""}::${messagesConfigVersion}`
      : lastIdentity;
    if (lastIdentity !== null && next !== null && next !== lastIdentity) {
      loadedImagesFor.clear();
      viewModeFor.clear();
    }
    if (next !== null) lastIdentity = next;
  });

  // ---------- v2b: thread fetch + cache ----------

  // Per-conversation cache keyed by conversation_id. SvelteMap is reactive,
  // so reads like `threadCache.get(...)` re-run in $derived expressions
  // without manual reassignment dance.
  const threadCache = new SvelteMap<number, MessageSummary[]>();
  let loadingThread = $state(false);
  let threadError: string | null = $state(null);
  let threadGen = 0;

  // currentConversationId resolution chain - first non-null wins:
  //   1. selected list-row summary
  //   2. linked-messages row (only when conversation_id is present)
  //   3. any cached thread containing route.message
  //   4. resolved detail.conversation_id
  const currentConversationId = $derived.by<number | null>(() => {
    const id = messageIdFromRoute(route.message);
    if (id === null) return null;
    // (1) selected list-row summary
    const row = searchResult?.messages.find((m) => m.id === id);
    if (row !== undefined) return row.conversation_id;
    // (2) linked-messages row (linkedIndex rows carry a `message` snapshot
    //     which is a MessageLinkRef whose conversation_id is optional)
    if (route.view === "linked" && linkedIndex !== null) {
      const linkedRow = linkedIndex.find((r) => r.message.message_id === id);
      const linkedConv = linkedRow?.message.conversation_id;
      if (linkedConv !== undefined) return linkedConv;
    }
    // (3) cache hit (peer click within an already-fetched thread)
    for (const [convId, summaries] of threadCache) {
      if (summaries.some((m) => m.id === id)) return convId;
    }
    // (4) detail fallback
    if (detail !== null && detail.id === id) return detail.conversation_id;
    return null;
  });

  // Currently rendered thread (from the cache); null while in flight or
  // when conv_id is unknown.
  const currentThread = $derived.by<MessageSummary[] | null>(() => {
    if (currentConversationId === null) return null;
    return threadCache.get(currentConversationId) ?? null;
  });

  // Fetch the thread whenever (a) the live capability is ok AND the
  // threads_endpoint feature is on, (b) conv_id is known, and (c) the
  // cache does not already have it. Race-safe via threadGen - older
  // in-flight responses bail before mutating the cache.
  //
  // When capabilities leave the ok state (banner-only / reconfigure /
  // upstream goes down), drop any in-flight fetch AND clear the cache:
  // the next ok state may be a different backend whose conversation ids
  // overlap numerically but mean different threads. Leaving stale
  // summaries in the cache would let currentConversationId step (3)
  // resolve and render foreign data.
  $effect(() => {
    if (!capabilities.ok || !capabilities.features.threads_endpoint) {
      threadGen++;
      loadingThread = false;
      threadError = null;
      if (threadCache.size > 0) threadCache.clear();
      return;
    }
    const convId = currentConversationId;
    if (convId === null) {
      threadGen++;
      loadingThread = false;
      threadError = null;
      return;
    }
    if (threadCache.has(convId)) {
      threadGen++;
      loadingThread = false;
      threadError = null;
      return;
    }
    const gen = ++threadGen;
    loadingThread = true;
    threadError = null;
    void messagesApi
      .thread(convId)
      .then((summaries) => {
        if (gen !== threadGen) return;
        threadCache.set(convId, summaries);
      })
      .catch((err: unknown) => {
        if (gen !== threadGen) return;
        threadError = err instanceof Error ? err.message : "Failed to load conversation context.";
      })
      .finally(() => {
        if (gen === threadGen) loadingThread = false;
      });
  });

  // Fetch all open issues once on mount so we can build the per-message reverse-link index.
  let linkedIssues: IssueSummary[] | null = $state(null);
  let linkedIssuesLoading = $state(false);
  let linkedIssuesError: string | null = $state(null);
  // Terminal first-attempt flag: set true once the initial auto-load resolves
  // (success OR failure) so the mount $effect can never auto-retry in a loop.
  let linkedIssuesLoaded = $state(false);

  // null while never loaded; otherwise the grouped rows (empty array = nothing linked).
  const linkedIndex = $derived(
    linkedIssues === null ? null : buildLinkedMessagesIndex(linkedIssues),
  );

  // Generation counter for refresh races: if a successful link triggers
  // refreshLinkedIssues() while the initial auto-load is still in flight,
  // both calls eventually resolve. Without this guard, the older response
  // could overwrite the newer one and re-stale the index.
  let linkedIssuesRefreshGen = 0;

  async function refreshLinkedIssues(): Promise<void> {
    if (!kata) return;
    const myGen = ++linkedIssuesRefreshGen;
    linkedIssuesLoading = true;
    linkedIssuesError = null;
    try {
      const res = await kata.search({
        scope: { kind: "all" },
        status: "all",
        owner: "",
        label: "",
        query: "",
      });
      if (myGen !== linkedIssuesRefreshGen) return; // a newer refresh raced ahead
      linkedIssues = res.issues;
    } catch (err) {
      if (myGen !== linkedIssuesRefreshGen) return;
      linkedIssuesError = err instanceof Error ? err.message : "Failed to load linked issues.";
      // Leave linkedIssues at its prior value so a refresh failure doesn't wipe the last good index.
    } finally {
      if (myGen === linkedIssuesRefreshGen) {
        linkedIssuesLoading = false;
        linkedIssuesLoaded = true;
      }
    }
  }

  $effect(() => {
    // Guard on linkedIssuesLoaded (terminal), NOT on linkedIssues === null -
    // a failed first fetch leaves linkedIssues null, so a === null guard would
    // re-fire forever. After the first attempt resolves, recovery is the
    // explicit Refresh button in the linked-messages view.
    if (kata && !linkedIssuesLoaded && !linkedIssuesLoading) {
      void refreshLinkedIssues();
    }
  });

  // Wrap onLinkMessage so a successful link refreshes the reverse index.
  async function handleLinkMessage(
    issueUid: string,
    input: MessageLinkInput,
  ): Promise<{ qualified_id: string }> {
    if (!onLinkMessage) throw new Error("link unavailable");
    const result = await onLinkMessage(issueUid, input);
    void refreshLinkedIssues();
    return result;
  }

  // Subset of linked issues for the currently-viewed message, passed to
  // MessageDetail's reverse pills as the "Linked to" section.
  const reverseLinksForCurrent = $derived.by(() => {
    const id = messageIdFromRoute(route.message);
    if (id === null || linkedIssues === null) return [];
    return findIssuesLinkedToMessage(linkedIssues, id);
  });

  function handleSearch(query: string, mode: MessagesSearchMode): void {
    searchMode = mode;
    // Normalize the typed query against route.q so an empty submit
    // clears the query (route.q <- null) but only if the route actually
    // had a query to clear. Without the comparison, an empty submit
    // with no prior query would churn the URL for a no-op; with it,
    // the previous "if (!trimmed) return" in the search bar can stay
    // gone - the parent owns the route, so the parent decides whether
    // the new value warrants an update.
    const nextQ = query === "" ? null : query;
    if (nextQ === (route.q ?? null) && route.message === null && route.view !== "linked") return;
    // Clear route.message and route.view on every effective search submit:
    // the previously focused message may no longer appear in the filtered
    // list, and showing a detail pane for a message that's missing
    // from the list is confusing. Same rationale as facet click (v1c).
    onRouteChange({ ...route, view: undefined, q: nextQ, message: null });
  }

  function handleSelect(id: number): void {
    onRouteChange({ ...route, message: String(id) });
  }

  // Facet click -> search bar round-trip. Same rationale as handleSearch
  // for clearing route.message: the focused message may no longer appear
  // in the now-filtered list. Skip the no-op route change when the token is
  // already in the query - UNLESS we're on the linked view, where the click
  // must still fire to clear `view` and return to the list.
  function handleSelectFacet(token: string): void {
    const current = route.q ?? "";
    const next = addFilterToQuery(current, token);
    if (next === current && route.view !== "linked") return;
    onRouteChange({ ...route, view: undefined, q: next || null, message: null });
  }

  let savedSearches = $state<SavedSearch[]>([]);
  let savedSearchesETag = $state<string | undefined>(undefined);
  let savedSearchesBanner = $state<string | null>(null);

  async function hydrateSavedSearches(): Promise<void> {
    try {
      const res = await savedSearchesApi.list();
      savedSearches = res.searches;
      savedSearchesETag = res.etag;
      savedSearchesBanner = null;
    } catch {
      savedSearchesBanner =
        "Saved searches couldn't be loaded; changes won't persist this session.";
    }
  }

  // Tracks the in-flight hydrate so a user save that fires before the
  // initial list() resolves doesn't PUT without an If-Match and clobber
  // existing daemon state (the handler accepts un-conditioned PUTs as
  // last-writer-wins; gating writes on the SPA side is what makes the
  // overall design safe). Per roborev Job 20501.
  let hydrationPromise: Promise<void> | undefined;

  $effect(() => {
    hydrationPromise = hydrateSavedSearches();
  });

  async function pushSavedSearches(
    apply: (current: SavedSearch[]) => SavedSearch[],
  ): Promise<void> {
    // Block on the initial hydration so we always carry a fresh ETag
    // into replace(). A pending list() must complete first; a failed
    // list() leaves savedSearchesETag undefined and the banner set,
    // and the user's optimistic add is preserved in memory by the
    // catch branch below.
    if (hydrationPromise) {
      try {
        await hydrationPromise;
      } catch {
        // hydrateSavedSearches itself never throws (it catches list()
        // errors and sets the banner) but guard defensively so a
        // future change can't accidentally throw here.
      }
    }
    // Re-apply the user's action against the post-hydration state
    // (per roborev Job 20507). If we captured `next` before awaiting,
    // a list() that resolved with existing entries during the await
    // would be silently overwritten - the PUT would carry only the
    // pre-hydration payload and drop everything else.
    const previous = savedSearches;
    const next = apply(previous);
    if (next === previous) {
      // The user's action is a no-op against the hydrated state (e.g.
      // delete on an entry that doesn't exist on the daemon). Skip
      // the PUT.
      return;
    }
    // Optimistic local update so the UI feels instant.
    savedSearches = next;
    // Degraded mode: list() failed and we have no server ETag. Skip
    // the network entirely so a quick save can't accidentally
    // overwrite real daemon state once it comes back online (PUT
    // without If-Match is last-writer-wins per the spec). Per roborev
    // Job 20503.
    if (savedSearchesETag === undefined) {
      savedSearchesBanner =
        savedSearchesBanner ??
        "Saved searches couldn't be saved; changes won't persist this session.";
      return;
    }
    try {
      const res = await savedSearchesApi.replace(next, savedSearchesETag);
      savedSearches = res.searches;
      savedSearchesETag = res.etag;
      savedSearchesBanner = null;
      return;
    } catch (err) {
      const apiErr = err as SavedSearchesAPIError;
      if (apiErr?.status === 412) {
        try {
          const fresh = await savedSearchesApi.list();
          // Replay: rebase the just-attempted change onto the fresh list.
          const replayed = computeReplay(fresh.searches, previous, next);
          const retry = await savedSearchesApi.replace(replayed, fresh.etag);
          savedSearches = retry.searches;
          savedSearchesETag = retry.etag;
          savedSearchesBanner = null;
          return;
        } catch {
          // Fall through to the in-memory + banner path below.
        }
      }
      // Save failed (network/500/repeated 412). Keep the optimistic next
      // state so the user doesn't lose their work - they can copy-paste
      // out if needed. Surface the muted banner so they know writes are
      // not persisting this session. Per roborev Job 20492.
      savedSearchesBanner =
        "Saved searches couldn't be saved; changes won't persist this session.";
    }
  }

  function computeReplay(
    fresh: SavedSearch[],
    before: SavedSearch[],
    after: SavedSearch[],
  ): SavedSearch[] {
    // Diff before -> after to find the single user action (add or remove).
    if (after.length > before.length) {
      const added = after.find(
        (a) => !before.some((b) => b.name.toLowerCase() === a.name.toLowerCase()),
      );
      return added ? addSavedSearch(fresh, added.name, added.query) : fresh;
    }
    if (after.length < before.length) {
      const removed = before.find(
        (b) => !after.some((a) => a.name.toLowerCase() === b.name.toLowerCase()),
      );
      return removed ? removeSavedSearch(fresh, removed.name) : fresh;
    }
    // Same length but content differs (in-place name replace): apply each
    // changed entry as an add (which dedupes-in-place).
    let out = fresh;
    for (const a of after) {
      const peer = before.find(
        (b) => b.name.toLowerCase() === a.name.toLowerCase(),
      );
      if (!peer || peer.query !== a.query) {
        out = addSavedSearch(out, a.name, a.query);
      }
    }
    return out;
  }

  function applyQuery(query: string): void {
    const nextQ = query.trim() || null;
    onRouteChange({ ...route, view: undefined, q: nextQ, message: null });
  }

  function handleSaveSearch(name: string, query: string): void {
    // Pass the action (not the precomputed next list) so pushSavedSearches
    // can replay it against the post-hydration state - a list() that
    // resolves between the click and the PUT must not be silently
    // overwritten. Per roborev Job 20507.
    void pushSavedSearches((current) => addSavedSearch(current, name, query));
  }

  function handleDeleteSearch(name: string): void {
    void pushSavedSearches((current) => removeSavedSearch(current, name));
  }

  const LAYOUT_KEY = "middleman:messagesLayout/v1";
  const DEFAULT_LIST_SIZE = 360;
  const MIN_PRIMARY = 220;

  function readListSize(): number {
    if (typeof localStorage === "undefined") return DEFAULT_LIST_SIZE;
    try {
      const raw = localStorage.getItem(LAYOUT_KEY);
      if (!raw) return DEFAULT_LIST_SIZE;
      const parsed = Number(raw);
      return Number.isFinite(parsed) ? parsed : DEFAULT_LIST_SIZE;
    } catch {
      return DEFAULT_LIST_SIZE;
    }
  }

  function writeListSize(size: number): void {
    if (typeof localStorage === "undefined") return;
    try {
      localStorage.setItem(LAYOUT_KEY, String(size));
    } catch {
      // Storage write blocked - layout still updates in memory.
    }
  }

  let listSize = $state(readListSize());
  let resizeStartSize = 0;

  function handleResize(size: number): void {
    listSize = size;
    writeListSize(size);
  }

  function startListResize(): void {
    resizeStartSize = listSize;
  }

  function resizeList(event: SplitResizeEvent): void {
    handleResize(resizeStartSize + event.deltaX);
  }

  let bannerStatus = $derived(
    capabilities.status === "down" ||
    capabilities.status === "unauthorized" ||
    capabilities.status === "misconfigured"
      ? capabilities.status
      : null,
  );

  // The workspace shell renders only when capabilities are explicitly ok.
  // Without this gate, `configured:false, ok:false, status: undefined`
  // (an absent or unknown msgvault config) would fall into the {:else}
  // branch and show a workspace shell for a backend that can't serve it.
  let workspaceReady = $derived(capabilities.status === "ok");
</script>

<div class="messages-workspace">
  {#if bannerStatus}
    <MessagesBanner
      status={bannerStatus}
      statusDetail={capabilities.status_detail}
      {onConfigure}
    />
  {:else if workspaceReady}
    <div class="messages-search-row">
      <MessagesSearchBar
        {capabilities}
        initialQuery={route.q ?? ""}
        onSubmit={handleSearch}
      />
    </div>
    <div class="messages-content-row">
      <aside class="messages-sidebar">
        {#if savedSearchesBanner}
          <p class="saved-searches-banner" role="status">{savedSearchesBanner}</p>
        {/if}
        <MessagesSavedViews
          quickViews={QUICK_VIEWS}
          {savedSearches}
          currentQuery={route.q ?? ""}
          onApply={applyQuery}
          onSave={handleSaveSearch}
          onDelete={handleDeleteSearch}
        />
        <MessagesFacets
          senders={{ rows: senders }}
          labels={{ rows: labels }}
          domains={{ rows: domains }}
          error={facetsError}
          onSelectFacet={handleSelectFacet}
          showLinkedView={kata !== undefined}
          activeView={route.view === "linked" ? "linked" : null}
          onSelectView={(v) => onRouteChange({
            ...route,
            view: v ?? undefined,
            // Clear the focused message when switching views - search-results
            // and linked-messages have different populations, so a stale
            // route.message can point at something not in the new list.
            message: null,
          })}
        />
      </aside>
      <div class="messages-sash-wrapper">
        <div class="messages-pane messages-pane-list" style:flex-basis={`${listSize}px`}>
          {#if kata !== undefined && route.view === "linked"}
            <LinkedMessagesView
              rows={linkedIndex}
              loading={linkedIssuesLoading}
              error={linkedIssuesError}
              selectedMessageId={messageIdFromRoute(route.message)}
              onRefresh={refreshLinkedIssues}
              onSelectMessage={(id) => onRouteChange({ ...route, message: String(id) })}
              onOpenIssue={(uid) => onOpenIssue?.(uid)}
            />
          {:else}
            {#if searchError}
              <div class="messages-list-error" role="alert">Search failed: {searchError}</div>
            {/if}
            <MessagesList
              messages={searchResult?.messages ?? []}
              {selectedID}
              loading={loadingList}
              onSelect={handleSelect}
            />
          {/if}
        </div>
        <SplitResizeHandle
          ariaLabel="Resize messages message list"
          onResizeStart={startListResize}
          onResize={resizeList}
        />
        <div class="messages-pane messages-pane-detail">
          {#if capabilities.ok && capabilities.features.threads_endpoint && currentConversationId !== null}
            <MessageThread
              detail={detail !== null && detail.id === selectedID ? detail : null}
              thread={currentThread}
              selectedMessageId={selectedID}
              onSelectMessage={handleSelect}
              {loadingDetail}
              {detailError}
              {loadingThread}
              {threadError}
              permalinkOf={(id) => `messages:msgvault:${id}`}
              remoteImageURL={messagesApi.remoteImageURL}
              {kata}
              onLinkMessage={onLinkMessage ? handleLinkMessage : undefined}
              reverseLinks={reverseLinksForCurrent}
              {onOpenIssue}
              imagesLoaded={detail !== null && loadedImagesFor.has(imageConsentKey(detail.id, detail.remote_image_token ?? ""))}
              onLoadImages={(id: number, token: string) => loadedImagesFor.add(imageConsentKey(id, token))}
              viewMode={detail !== null ? (viewModeFor.get(detail.id) ?? "html") : "html"}
              onViewModeChange={(id: number, m: "html" | "text") => viewModeFor.set(id, m)}
              remoteImageCount={detail?.remote_image_count ?? 0}
              remoteImageToken={detail?.remote_image_token ?? ""}
              htmlSanitizationFailed={detail?.html_sanitization_failed === true}
            />
          {:else}
            <MessageDetail
              {detail}
              loading={loadingDetail}
              error={detailError}
              permalinkOf={(id) => `messages:msgvault:${id}`}
              remoteImageURL={messagesApi.remoteImageURL}
              {kata}
              onLinkMessage={onLinkMessage ? handleLinkMessage : undefined}
              reverseLinks={reverseLinksForCurrent}
              {onOpenIssue}
              imagesLoaded={detail !== null && loadedImagesFor.has(imageConsentKey(detail.id, detail.remote_image_token ?? ""))}
              onLoadImages={(id: number, token: string) => loadedImagesFor.add(imageConsentKey(id, token))}
              viewMode={detail !== null ? (viewModeFor.get(detail.id) ?? "html") : "html"}
              onViewModeChange={(id: number, m: "html" | "text") => viewModeFor.set(id, m)}
              remoteImageCount={detail?.remote_image_count ?? 0}
              remoteImageToken={detail?.remote_image_token ?? ""}
              htmlSanitizationFailed={detail?.html_sanitization_failed === true}
            />
          {/if}
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  .messages-workspace {
    display: grid;
    grid-template-rows: auto 1fr;
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }

  .messages-search-row {
    border-bottom: 1px solid var(--border-default);
    padding: 8px 12px;
  }

  .messages-content-row {
    /* 240px facet sidebar + remaining width for the list/detail sash. */
    display: grid;
    grid-template-columns: 240px minmax(0, 1fr);
    min-height: 0;
    overflow: hidden;
  }

  .messages-sidebar {
    border-right: 1px solid var(--border-default);
    overflow-y: auto;
    min-width: 0;
    background: var(--bg-primary);
  }

  .messages-sash-wrapper {
    display: flex;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .messages-pane {
    flex: 1;
    overflow: hidden;
  }

  .messages-pane-list {
    flex: 0 0 auto;
    min-width: 220px;
  }

  .messages-pane-detail {
    min-width: 320px;
  }

  .messages-list-error {
    padding: 8px 12px;
    font-size: var(--font-size-xs);
    color: var(--accent-red);
    background: var(--accent-red-soft);
    border-bottom: 1px solid var(--accent-red);
  }

  .saved-searches-banner {
    margin: 0 0 8px;
    padding: 6px 10px;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
    background: var(--bg-inset, #f5f5f5);
    border-radius: 4px;
  }

  @media (max-width: 899px) {
    /* The sidebar column lives on .messages-content-row (the search bar spans
       both columns above it), so the responsive reset must target the same
       grid container - resetting .messages-workspace here would leave the
       explicit 240px track in place at narrow widths. */
    .messages-content-row {
      grid-template-columns: minmax(0, 1fr);
    }
    .messages-sidebar {
      display: none;
    }
  }
</style>
