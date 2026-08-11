<script lang="ts">
  import Download from "@lucide/svelte/icons/download";
  import FileText from "@lucide/svelte/icons/file-text";
  import MoreHorizontal from "@lucide/svelte/icons/more-horizontal";
  import PanelRight from "@lucide/svelte/icons/panel-right";
  import PanelRightClose from "@lucide/svelte/icons/panel-right-close";
  import Pencil from "@lucide/svelte/icons/pencil";
  import Plus from "@lucide/svelte/icons/plus";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import Upload from "@lucide/svelte/icons/upload";
  import { Effect, Option } from "effect";
  import { showFlash } from "../../stores/flash.svelte.js";
  import type { DocsRoute } from "../../api/docs/route.js";
  import {
    createDocsAPI,
    DocsRequestError,
    executeDocsRequest,
    retryIdempotentDocsRequest,
    type DocsAPI,
  } from "../../api/docs/api";
  import { searchKataReferences, type KataReferenceSearch } from "../../api/kata/integration.js";
  import type {
    GitPublishResponse,
    GitPullResponse,
    GitStatusEntry,
    GitStatusResponse,
    TreeNode,
    Folder,
  } from "../../api/docs/types";
  import PublishDocsDialog from "./PublishDocsDialog.svelte";
  import { buildFolderIndex, type FolderIndex } from "../../api/docs/folderLinks";
  import { docsHref } from "../../api/docs/route.js";
  import { withBasePath } from "../../stores/router.svelte.js";
  import { onDestroy, untrack } from "svelte";
  import { createKataDaemonsStore } from "../../stores/kata-daemons.svelte.js";
  import DocMarkdownView, { type DocMarkdownState, type HeadingEntry } from "./DocMarkdownView.svelte";
  import DocOutline from "./DocOutline.svelte";
  import FolderTree from "./FolderTree.svelte";
  import Modal from "../shared/Modal.svelte";
  import AddFolderDialog from "./AddFolderDialog.svelte";
  import { buildIssueCompletionSource } from "./issueCompletion";
  import { buildDocsIssueCompletionOptions } from "./docsIssueCompletionOptions";
  import { effectiveDocsFolderDaemon } from "./folderDaemon";
  import { buildWikilinkCompletionSource } from "./wikilinkCompletion";
  import { SelectDropdown, type SelectDropdownOption } from "@kenn-io/kit-ui";
  import { IconButton } from "@kenn-io/kit-ui";
  import type { AppExecution, AppServices } from "../../app/runtime.js";
  import { getAppRuntime } from "../../app/runtime-context.js";
  import {
    docsMutationOwner,
    DocsMutationStateUncertainError,
    DocsWorkflow,
    makeDocsOwner,
  } from "../../stores/docs-workflow.js";
  import { flattenTreePaths } from "./folderTreePaths";

  interface Props {
    route: DocsRoute;
    onRouteChange: (next: DocsRoute, options?: { replace?: boolean }) => void;
    api?: DocsAPI | undefined;
    onOpenKataReference?: ((
      reference: string,
      project?: string,
      daemonId?: string,
      kind?: "reference" | "uid",
    ) => void) | undefined;
    searchReferences?: KataReferenceSearch | undefined;
  }

  let {
    route,
    onRouteChange,
    api = createDocsAPI(),
    onOpenKataReference,
    searchReferences = searchKataReferences,
  }: Props = $props();
  const runtime = getAppRuntime();
  const kataDaemons = createKataDaemonsStore();
  const docsOwner = makeDocsOwner("docs-workspace");
  const docsPresentationSurface = "docs-workspace";

  const issueCompletionSource = buildIssueCompletionSource(
    buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => route.folder,
      daemonRoster: () => kataDaemons.daemons().map((daemon) => daemon.id),
      searchReferences: (...args) => searchReferences(...args),
    }),
    runtime,
  );
  // Wikilink (`[[`) suggestions read the live folder index so docs
  // added or renamed during the session show up without remounting
  // the editor.
  const wikilinkCompletionSource = buildWikilinkCompletionSource(
    () => folderIndex,
  );

  let folders: Folder[] = $state([]);
  let foldersError: string | null = $state(null);
  // Separate loading state so the chip trigger only blocks while
  // listFolders() is in flight — an empty success should still be
  // reachable so the "No folders configured." menu state can render.
  let loadingFolders = $state(false);
  let tree: TreeNode | null = $state(null);
  let treeError: string | null = $state(null);
  let loadingTree = $state(false);
  // Inline-rename failure surface. The tree library invokes our handler
  // fire-and-forget, so the rejected promise has nowhere to bubble up to —
  // we capture the reason here and render it above the tree so the user
  // sees why the rename appeared to vanish after they pressed Enter.
  let inlineFileError: string | null = $state(null);

  // Per-folder git status entries. Loaded lazily alongside the tree;
  // missing / non-repo folders are stored as an empty array so the
  // tree renders without decoration but doesn't keep refetching.
  let gitEntriesByFolder: Record<string, readonly GitStatusEntry[]> = $state({});
  // Tracks whether each folder is a git repo. Populated alongside
  // gitEntriesByFolder by loadGitStatus.
  let folderIsRepo: Record<string, boolean> = $state({});

  let publishOpen = $state(false);
  let gitNotice: { kind: "success" | "error"; text: string } | null = $state(null);
  let pulling = $state(false);

  let lastFolderLoaded: string | null = null;
  let lastDocLoaded: string | null = null;
  // Tracks which folder we've already auto-opened a landing page for, so
  // the effect doesn't fight the user when they intentionally clear the
  // doc query and stay on the bare folder.
  let autoOpenedFor: string | null = null;

  let foldersExecution: AppExecution<void, never> | undefined;
  let treeExecution: AppExecution<void, never> | undefined;
  let gitExecution: AppExecution<void, never> | undefined;
  let docExecution: AppExecution<void, never> | undefined;
  let editorExecution: AppExecution<void, never> | undefined;

  let docContent: string | null = $state(null);
  // Identifies which (folder, doc) the current docContent belongs to.
  // beginEdit / saveEdit refuse to fire when this doesn't match the
  // live route, so a slow read can't smuggle the previous file's body
  // into the new path.
  let docContentKey: string | null = $state(null);
  let docError: string | null = $state(null);
  let loadingDoc = $state(false);
  // On direct navigation, refresh, or open-in-new-tab the target heading
  // arrives only in window.location.hash — in-app clicks route it through
  // selectDoc instead, and the router rebuilds the URL without a fragment,
  // so this seed only fires for the initial load. The anchor is one-shot:
  // DocMarkdownView fires onAnchorConsumed once it scrolls, clearing this
  // so a later folder switch / landing auto-open can't reuse the stale
  // anchor on an unrelated doc with a matching heading id.
  let pendingAnchor: string | null = $state(readInitialAnchor());

  function readInitialAnchor(): string | null {
    if (typeof window === "undefined") return null;
    const hash = window.location.hash;
    if (!hash || hash === "#") return null;
    const raw = hash.slice(1);
    try {
      return decodeURIComponent(raw);
    } catch {
      return raw;
    }
  }
  let headings: HeadingEntry[] = $state([]);
  let activeHeadingID: string | null = $state(null);

  // Outline visibility is a per-user preference, persisted in
  // localStorage so it survives reloads (Tasks↔Docs nav already
  // preserves it because App.svelte keeps both panels mounted).
  // localStorage may be missing (SSR/tests) or may throw on access in
  // restricted environments (private browsing, blocked cookies); both
  // paths fall back silently to the default. Default is visible so a
  // fresh user discovers the outline.
  const OUTLINE_COLLAPSED_KEY = "kenn-forge:docs:outline-collapsed";
  function readOutlineCollapsed(): boolean {
    if (typeof localStorage === "undefined") return false;
    try {
      return localStorage.getItem(OUTLINE_COLLAPSED_KEY) === "1";
    } catch {
      return false;
    }
  }
  let outlineCollapsed = $state(readOutlineCollapsed());
  function toggleOutline() {
    outlineCollapsed = !outlineCollapsed;
    if (typeof localStorage === "undefined") return;
    try {
      localStorage.setItem(OUTLINE_COLLAPSED_KEY, outlineCollapsed ? "1" : "0");
    } catch {
      // Storage write blocked — outline still toggles in memory.
    }
  }

  let editing = $state(false);
  let DocMarkdownEditor = $state<typeof import("./DocMarkdownEditor.svelte").default | null>(null);
  let editorLoading = $state(false);
  let editorLoadError: string | null = $state(null);
  let editorDraft = $state<string>("");
  let editorDirty = $state(false);
  let saving = $state(false);
  let saveError: string | null = $state(null);
  // Captured at beginEdit; saveEdit writes the buffer to this (folder,
  // doc) pair regardless of where the route has drifted to. docContentKey
  // alone is unsafe — it tracks the *loaded* doc and gets rewritten when
  // a route change triggers loadDoc, so a dirty draft would otherwise be
  // persistable into whichever doc happens to be in view at Save time.
  // Structured rather than a delimited string so folder ids or paths
  // containing "::" can't corrupt the round-trip.
  let editTarget: { folder: string; doc: string } | null = $state(null);

  function docKey(folder: string | null, doc: string | null): string {
    return JSON.stringify([folder ?? null, doc ?? null]);
  }

  function docsResourceKey(kind: "folders" | "tree" | "git-status" | "document", ...identity: string[]): string {
    return JSON.stringify([kind, ...identity]);
  }

  function docsIntentKey(operation: string, ...identity: string[]): string {
    return JSON.stringify([operation, ...identity]);
  }

  let currentRouteKey = $derived(docKey(route.folder, route.doc));
  let editReady = $derived(
    docContent !== null && docContentKey === currentRouteKey && !loadingDoc,
  );

  interface TreeReconciliation {
    readonly value: TreeNode | null;
    readonly error: DocsRequestError | null;
  }

  interface GitStatusReconciliation {
    readonly value: GitStatusResponse | null;
    readonly error: DocsRequestError | null;
  }

  interface DocumentReconciliation {
    readonly path: string;
    readonly value: string | null;
    readonly error: DocsRequestError | null;
  }

  interface PullConfirmation {
    readonly kind: "response";
    readonly result: GitPullResponse;
  }

  interface PullReconciliation {
    readonly confirmation: PullConfirmation;
    readonly tree: TreeReconciliation | null;
    readonly gitStatus: GitStatusReconciliation | null;
    readonly document: DocumentReconciliation | null;
  }

  interface PresentedCommandOptions<A, E> {
    readonly operation: string;
    readonly safeContext: Readonly<Record<string, string | number | boolean>>;
    readonly onFailure: (failure: E) => void;
    readonly onSuccess: (value: A) => void;
    readonly onDone?: (() => void) | undefined;
  }

  function runPresentedCommand<A, E>(
    program: Effect.Effect<A, E, AppServices>,
    options: PresentedCommandOptions<A, E>,
  ): void {
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* DocsWorkflow;
        const result = yield* program.pipe(
          Effect.match({
            onFailure: (failure) => ({ failed: failure }),
            onSuccess: (value) => ({ succeeded: value }),
          }),
        );
        yield* workflow.present(
          docsPresentationSurface,
          docsOwner,
          Effect.sync(() => {
            if ("failed" in result) options.onFailure(result.failed);
            else options.onSuccess(result.succeeded);
            options.onDone?.();
          }),
        );
      }),
      { operation: options.operation, safeContext: options.safeContext, onFailure: () => {} },
    );
  }

  let folderIndex: FolderIndex = $derived(buildFolderIndex(tree));

  function loadEditor(onLoaded?: () => void): void {
    if (DocMarkdownEditor) {
      onLoaded?.();
      return;
    }
    if (editorLoading) return;
    editorLoading = true;
    editorLoadError = null;
    let execution: AppExecution<void, never> | undefined;
    const isCurrent = () => execution !== undefined && editorExecution === execution;
    execution = runtime.runCommand(
      Effect.tryPromise({
        try: () => import("./DocMarkdownEditor.svelte"),
        catch: (cause) => (cause instanceof Error ? cause : new Error("Could not load editor.")),
      }).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              if (!isCurrent()) return;
              editorLoadError = failure.message || "Could not load editor.";
            }),
          onSuccess: (loaded) =>
            Effect.sync(() => {
              if (!isCurrent()) return;
              DocMarkdownEditor = loaded.default;
              onLoaded?.();
            }),
        }),
        Effect.ensuring(
          Effect.sync(() => {
            if (!isCurrent()) return;
            editorExecution = undefined;
            editorLoading = false;
          }),
        ),
      ),
      { operation: "load Docs editor", safeContext: {}, onFailure: () => {} },
    );
    editorExecution = execution;
  }

  $effect(() => {
    loadFolders();
  });

  runtime.runCommand(
    Effect.gen(function* () {
      const workflow = yield* DocsWorkflow;
      yield* workflow.claimPresenter(
        docsPresentationSurface,
        docsOwner,
        Effect.sync(refreshPresentedState),
      );
    }),
    { operation: "claim Docs workspace presentation", safeContext: { owner: docsOwner }, onFailure: () => {} },
  );

  onDestroy(() => {
    editorExecution?.interrupt();
    runtime.runCommand(
      Effect.gen(function* () {
        const workflow = yield* DocsWorkflow;
        yield* workflow.releasePresenter(docsPresentationSurface, docsOwner);
        yield* workflow.stop(docsOwner);
      }),
      { operation: "stop Docs workspace reads", safeContext: { owner: docsOwner }, onFailure: () => {} },
    );
  });

  // ] toggles the outline. Scoped to docs-mode by checking that the
  // workspace root is actually visible (App.svelte hides it via
  // display:none when in tasks mode, which sets offsetParent to null).
  // Input/textarea/contenteditable focus is skipped so the shortcut
  // doesn't fire while the user is typing.
  $effect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "]") return;
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      const target = event.target;
      if (target instanceof HTMLElement) {
        const tag = target.tagName;
        if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
        if (target.isContentEditable) return;
      }
      if (!workspaceRoot || workspaceRoot.offsetParent === null) return;
      event.preventDefault();
      toggleOutline();
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  });

  let workspaceRoot: HTMLDivElement | null = $state(null);

  $effect(() => {
    const folderID = route.folder;
    if (folderID === lastFolderLoaded) return;
    lastFolderLoaded = folderID;
    // Drop the old folder's tree immediately so the README auto-open
    // effect doesn't try to land a doc from the previous folder while
    // the new tree is in flight.
    tree = null;
    treeError = null;
    if (!folderID) return;
    loadTree(folderID);
    loadGitStatus(folderID);
  });

  // When the tree finishes loading and no doc is selected, look for a
  // root-level landing page (README.md / index.md / either casing) and
  // auto-open it with replaceState so the URL doesn't accumulate dead
  // history entries from folder visits.
  $effect(() => {
    if (!tree || route.doc !== null || !route.folder) return;
    if (autoOpenedFor === route.folder) return;
    autoOpenedFor = route.folder;
    const landing = findLandingDoc(tree);
    if (!landing) return;
    onRouteChange(
      { mode: "docs", folder: route.folder, doc: landing },
      { replace: true },
    );
  });

  $effect(() => {
    const folderID = route.folder;
    const docPath = route.doc;
    const key = docKey(folderID, docPath);
    if (key === lastDocLoaded) return;
    // Hold the loaded doc in place while a dirty draft is open so the
    // editor stays mounted on editTarget; otherwise loadDoc would unmount
    // the doc-pane (clears docContent) and remount with the new file's
    // body, letting a subsequent Save write the wrong file's content
    // back to editTarget. Don't advance lastDocLoaded so the effect
    // re-runs once Save or Cancel clears editing.
    if (editing && editorDirty) return;
    lastDocLoaded = key;
    // Clear the previous doc immediately so a slow load doesn't render
    // stale content under the new route, and so beginEdit can't capture
    // the previous body until the new content lands.
    docContent = null;
    docContentKey = null;
    docError = null;
    headings = [];
    activeHeadingID = null;
    if (!folderID || !docPath) return;
    loadDoc(folderID, docPath);
  });

  const readFolders = Effect.fn("DocsWorkspace.readFolders")(function* (
    requestedAPI: DocsAPI,
    owner = docsOwner,
  ) {
    const workflow = yield* DocsWorkflow;
    return yield* workflow.read(
      owner,
      { lane: "folders", resource: docsResourceKey("folders") },
      executeDocsRequest("list Docs folders", (signal) => requestedAPI.listFolders(signal)),
    );
  });

  const readTree = Effect.fn("DocsWorkspace.readTree")(function* (
    folderID: string,
    requestedAPI: DocsAPI,
    owner = docsOwner,
  ) {
    const workflow = yield* DocsWorkflow;
    return yield* workflow.read(
      owner,
      { lane: "tree", resource: docsResourceKey("tree", folderID) },
      executeDocsRequest("load Docs tree", (signal) => requestedAPI.tree(folderID, signal)),
    );
  });

  const readGitStatus = Effect.fn("DocsWorkspace.readGitStatus")(function* (
    folderID: string,
    requestedAPI: DocsAPI,
    owner = docsOwner,
  ) {
    const workflow = yield* DocsWorkflow;
    return yield* workflow.read(
      owner,
      { lane: "git-status", resource: docsResourceKey("git-status", folderID) },
      executeDocsRequest("load Docs git status", (signal) => requestedAPI.gitStatus(folderID, signal)),
    );
  });

  const readDoc = Effect.fn("DocsWorkspace.readDocument")(function* (
    folderID: string,
    docPath: string,
    requestedAPI: DocsAPI,
    owner = docsOwner,
  ) {
    const workflow = yield* DocsWorkflow;
    return yield* workflow.read(
      owner,
      { lane: "document", resource: docsResourceKey("document", folderID, docPath) },
      executeDocsRequest("read Docs document", (signal) => requestedAPI.readFile(folderID, docPath, signal)),
    );
  });

  const queueDocsMutation = Effect.fn("DocsWorkspace.queueMutation")(function* <A, E, R>(
    mutation: Effect.Effect<A, E, R>,
  ) {
    const workflow = yield* DocsWorkflow;
    return yield* workflow.mutate(docsPresentationSurface, docsOwner, mutation);
  });

  const reconcileTree = Effect.fn("DocsWorkspace.reconcileTree")(function* (
    folderID: string,
    requestedAPI: DocsAPI,
  ) {
    return yield* readTree(folderID, requestedAPI, docsMutationOwner).pipe(
      Effect.match({
        onFailure: (error): TreeReconciliation => ({ value: null, error }),
        onSuccess: (value): TreeReconciliation => ({ value, error: null }),
      }),
    );
  });

  const reconcileGitStatus = Effect.fn("DocsWorkspace.reconcileGitStatus")(function* (
    folderID: string,
    requestedAPI: DocsAPI,
  ) {
    return yield* readGitStatus(folderID, requestedAPI, docsMutationOwner).pipe(
      Effect.match({
        onFailure: (error): GitStatusReconciliation => ({ value: null, error }),
        onSuccess: (value): GitStatusReconciliation => ({ value, error: null }),
      }),
    );
  });

  const reconcileDocument = Effect.fn("DocsWorkspace.reconcileDocument")(function* (
    folderID: string,
    docPath: string,
    requestedAPI: DocsAPI,
  ) {
    return yield* readDoc(folderID, docPath, requestedAPI, docsMutationOwner).pipe(
      Effect.match({
        onFailure: (error): DocumentReconciliation => ({ path: docPath, value: null, error }),
        onSuccess: (value): DocumentReconciliation => ({ path: docPath, value, error: null }),
      }),
    );
  });

  function applyTreeReconciliation(folderID: string, reconciliation: TreeReconciliation): void {
    if (route.folder !== folderID) return;
    if (reconciliation.error !== null) {
      tree = null;
      treeError = reconciliation.error.message || "Failed to load tree";
      return;
    }
    tree = reconciliation.value;
    treeError = null;
  }

  function applyGitStatusReconciliation(folderID: string, reconciliation: GitStatusReconciliation): void {
    if (route.folder !== folderID) return;
    if (reconciliation.error !== null) {
      gitEntriesByFolder = { ...gitEntriesByFolder, [folderID]: [] };
      folderIsRepo = {
        ...folderIsRepo,
        [folderID]: reconciliation.error.code === "unsafe_git_config",
      };
      return;
    }
    if (reconciliation.value === null) return;
    gitEntriesByFolder = { ...gitEntriesByFolder, [folderID]: reconciliation.value.entries };
    folderIsRepo = { ...folderIsRepo, [folderID]: reconciliation.value.is_repo };
  }

  function applyDocumentReconciliation(folderID: string, reconciliation: DocumentReconciliation): void {
    if (route.folder !== folderID || route.doc !== reconciliation.path || editing) return;
    if (reconciliation.error !== null) {
      docContent = null;
      docContentKey = null;
      docError = reconciliation.error.message || "Failed to load document";
      return;
    }
    docContent = reconciliation.value;
    docContentKey = docKey(folderID, reconciliation.path);
    docError = null;
  }

  function loadFolders(): void {
    loadingFolders = true;
    const requestedAPI = api;
    let execution: AppExecution<void, never> | undefined;
    const isCurrent = () => execution !== undefined && foldersExecution === execution;
    execution = runtime.runCommand(
      readFolders(requestedAPI).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              if (!isCurrent()) return;
              foldersError = failure.message || "Failed to load folders";
            }),
          onSuccess: (result) =>
            Effect.sync(() => {
              if (!isCurrent()) return;
              folders = result;
              foldersError = null;
              const routeFolderExists = route.folder !== null && result.some((folder) => folder.id === route.folder);
              if (route.folder && !routeFolderExists) {
                onRouteChange({ mode: "docs", folder: result[0]?.id ?? null, doc: null }, { replace: true });
              } else if (!route.folder && result.length > 0) {
                const target = result[0]!.id;
                const targetDoc = route.doc;
                if (targetDoc) autoOpenedFor = target;
                onRouteChange({ mode: "docs", folder: target, doc: targetDoc }, { replace: true });
              }
            }),
        }),
        Effect.ensuring(
          Effect.sync(() => {
            if (!isCurrent()) return;
            foldersExecution = undefined;
            loadingFolders = false;
          }),
        ),
      ),
      { operation: "load Docs folders", safeContext: { owner: docsOwner }, onFailure: () => {} },
    );
    foldersExecution = execution;
  }

  function refreshPresentedState(): void {
    loadFolders();
    const folderID = route.folder;
    if (!folderID) return;
    loadTree(folderID);
    loadGitStatus(folderID);
    if (route.doc && !editing) loadDoc(folderID, route.doc);
  }

  function loadTree(folderID: string): void {
    loadingTree = true;
    treeError = null;
    const requestedAPI = api;
    let execution: AppExecution<void, never> | undefined;
    const isCurrent = () => execution !== undefined && treeExecution === execution && route.folder === folderID;
    execution = runtime.runCommand(
      readTree(folderID, requestedAPI).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              if (!isCurrent()) return;
              tree = null;
              treeError = failure.message || "Failed to load tree";
            }),
          onSuccess: (result) =>
            Effect.sync(() => {
              if (isCurrent()) tree = result;
            }),
        }),
        Effect.ensuring(
          Effect.sync(() => {
            if (!isCurrent()) return;
            treeExecution = undefined;
            loadingTree = false;
          }),
        ),
      ),
      { operation: "load Docs tree", safeContext: { owner: docsOwner, folderID }, onFailure: () => {} },
    );
    treeExecution = execution;
  }

  function loadGitStatus(folderID: string): void {
    const requestedAPI = api;
    let execution: AppExecution<void, never> | undefined;
    const isCurrent = () => execution !== undefined && gitExecution === execution && route.folder === folderID;
    execution = runtime.runCommand(
      readGitStatus(folderID, requestedAPI).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              if (!isCurrent()) return;
              gitEntriesByFolder = { ...gitEntriesByFolder, [folderID]: [] };
              folderIsRepo = { ...folderIsRepo, [folderID]: failure.code === "unsafe_git_config" };
            }),
          onSuccess: (result) =>
            Effect.sync(() => {
              if (!isCurrent()) return;
              gitEntriesByFolder = { ...gitEntriesByFolder, [folderID]: result.entries };
              folderIsRepo = { ...folderIsRepo, [folderID]: result.is_repo };
            }),
        }),
        Effect.ensuring(
          Effect.sync(() => {
            if (isCurrent()) gitExecution = undefined;
          }),
        ),
      ),
      { operation: "load Docs git status", safeContext: { owner: docsOwner, folderID }, onFailure: () => {} },
    );
    gitExecution = execution;
  }

  function loadDoc(folderID: string, docPath: string): void {
    loadingDoc = true;
    docError = null;
    const requestedAPI = api;
    const requestedKey = docKey(folderID, docPath);
    let execution: AppExecution<void, never> | undefined;
    const isCurrent = () => execution !== undefined && docExecution === execution && currentRouteKey === requestedKey;
    execution = runtime.runCommand(
      readDoc(folderID, docPath, requestedAPI).pipe(
        Effect.matchEffect({
          onFailure: (failure) =>
            Effect.sync(() => {
              if (!isCurrent()) return;
              docContent = null;
              docContentKey = null;
              docError = failure.message || "Failed to load document";
            }),
          onSuccess: (content) =>
            Effect.sync(() => {
              if (!isCurrent()) return;
              docContent = content;
              docContentKey = requestedKey;
            }),
        }),
        Effect.ensuring(
          Effect.sync(() => {
            if (!isCurrent()) return;
            docExecution = undefined;
            loadingDoc = false;
          }),
        ),
      ),
      { operation: "load Docs document", safeContext: { owner: docsOwner, folderID, docPath }, onFailure: () => {} },
    );
    docExecution = execution;
  }

  // Order matches the user-approved spec: capital README first (most
  // common in code repos), then lowercase, then index.md / Index.md.
  const LANDING_CANDIDATES = ["README.md", "readme.md", "index.md", "Index.md"];

  function findLandingDoc(root: TreeNode): string | null {
    for (const candidate of LANDING_CANDIDATES) {
      const hit = (root.children ?? []).find(
        (child) => !child.is_dir && child.name === candidate,
      );
      if (hit) return hit.rel_path;
    }
    return null;
  }


  function selectFolder(id: string) {
    if (id === route.folder) return;
    onRouteChange({ mode: "docs", folder: id, doc: null });
  }

  function selectDoc(relPath: string, anchor?: string) {
    pendingAnchor = anchor ?? null;
    if (relPath === route.doc) {
      // Already viewing this doc — just scroll to the anchor if requested.
      return;
    }
    onRouteChange({ mode: "docs", folder: route.folder, doc: relPath });
  }

  function selectHeading(id: string) {
    pendingAnchor = id;
  }

  function handleMarkdownState(state: DocMarkdownState) {
    headings = state.headings;
    activeHeadingID = state.activeId;
  }

  function buildDocURL(folderID: string, docPath: string, anchor?: string): string {
    const url = withBasePath(docsHref({ mode: "docs", folder: folderID, doc: docPath }));
    return anchor ? `${url}#${encodeURIComponent(anchor)}` : url;
  }

  function buildBlobURL(folderID: string, relPath: string): string {
    return api.blobURL(folderID, relPath);
  }

  function handleKataReference(reference: string, project?: string, kind?: "reference" | "uid") {
    onOpenKataReference?.(reference, project, folderDaemon(), kind);
  }

  function folderDaemon(): string | undefined {
    return effectiveDocsFolderDaemon(
      folders,
      route.folder,
      kataDaemons.daemons().map((daemon) => daemon.id),
    );
  }

  $effect(() => {
    const controller = new AbortController();
    void kataDaemons.load(controller.signal);
    return () => controller.abort();
  });

  function beginEdit(): void {
    // Refuse to enter edit mode unless the loaded body belongs to the
    // route currently in view — guards against the race where the user
    // navigates to doc B, then clicks Edit before B has finished
    // loading, and edits A's body into B's path.
    if (!editReady || docContent === null) return;
    if (!route.folder || !route.doc) return;
    // A pull may rewrite this doc on disk; an editor opened mid-pull would
    // capture the pre-pull body and a later save would overwrite the pulled
    // content with it. The Edit button is disabled while pulling; re-check
    // after the lazy import too, since a pull can start while it loads.
    if (pulling) return;
    const folderID = route.folder;
    const docPath = route.doc;
    loadEditor(() => {
      if (!DocMarkdownEditor || pulling || !editReady || docContent === null) return;
      if (route.folder !== folderID || route.doc !== docPath) return;
      editorDraft = docContent;
      editorDirty = false;
      saveError = null;
      editing = true;
      editTarget = { folder: folderID, doc: docPath };
    });
  }

  function cancelEdit() {
    if (editorDirty) {
      const ok = confirm("Discard unsaved changes?");
      if (!ok) return;
    }
    editing = false;
    editorDirty = false;
    saveError = null;
    editTarget = null;
  }

  function handleEditorChange(value: string, dirty: boolean) {
    editorDraft = value;
    editorDirty = dirty;
  }

  function saveEdit(value: string): void {
    if (saving) return;
    if (!editTarget) {
      saveError = "Editor not initialised; reopen the document before saving.";
      return;
    }
    const target = editTarget;
    // The route may have changed under us between opening the editor
    // and the user hitting Save. We persist against the doc the editor
    // was opened on (editTarget) — never the live route — so a
    // navigation can't retarget the buffer to a different file.
    saving = true;
    saveError = null;
    const requestedAPI = api;
    runPresentedCommand(
      queueDocsMutation(
        Effect.gen(function* () {
          const workflow = yield* DocsWorkflow;
          return yield* workflow.reconcileMutation(
            {
              resource: docsResourceKey("document", target.folder, target.doc),
              intent: docsIntentKey("save", target.folder, target.doc, value),
            },
            "save Docs document",
            executeDocsRequest("save Docs document", (signal) =>
              requestedAPI.writeFile(target.folder, target.doc, value, signal),
            ),
            readDoc(target.folder, target.doc, requestedAPI, docsMutationOwner),
            (content) => (content === value ? Option.some(undefined) : Option.none()),
          );
        }),
      ),
      {
        operation: "save Docs document",
        safeContext: { folderID: target.folder, docPath: target.doc },
        onFailure: (failure) => {
          if (editTarget?.folder !== target.folder || editTarget.doc !== target.doc) return;
          const message = describeFileError(failure, "Failed to save");
          if (isFileInputError(failure)) saveError = message;
          else showFlash(message, { tone: "danger" });
        },
        onSuccess: (outcome) => {
          if (editTarget?.folder !== target.folder || editTarget.doc !== target.doc) return;
          const targetKey = docKey(target.folder, target.doc);
          const currentKey = docKey(route.folder, route.doc);
          if (currentKey === targetKey) {
            docContent = outcome.snapshot;
            docContentKey = currentKey;
          }
          editing = false;
          editorDirty = false;
          editTarget = null;
        },
        onDone: () => {
          saving = false;
        },
      },
    );
  }

  // Exit edit mode whenever the route navigates to a different doc.
  // If the draft is dirty we want the user to either confirm discard
  // or stash the buffer with a save-error so it isn't silently dropped.
  // `untrack` keeps this effect from re-firing when `editing` itself
  // changes — only route changes should reset.
  $effect(() => {
    void route.folder;
    void route.doc;
    untrack(() => {
      if (!editing) return;
      if (editorDirty) {
        // Keep editing flag on so the dirty buffer stays visible; the
        // user must explicitly Save or Cancel before navigating away
        // takes the editor with it. Surface a save-error banner so the
        // intent isn't silent.
        saveError = "Unsaved changes — Save or Cancel before navigating.";
        return;
      }
      editing = false;
      saveError = null;
      editTarget = null;
    });
  });

  let activeFolder = $derived(folders.find((folder) => folder.id === route.folder));
  let folderOptions = $derived<SelectDropdownOption[]>(
    folders.map((folder) => ({ value: folder.id, label: folder.name })),
  );
  let staleFolderDaemon = $derived.by(() => {
    if (!kataDaemons.loaded()) return undefined;
    const daemon = activeFolder?.daemon?.trim();
    if (!daemon) return undefined;
    return kataDaemons.daemons().some((candidate) => candidate.id === daemon) ? undefined : daemon;
  });

  let activeFolderGit = $derived(
    route.folder ? gitEntriesByFolder[route.folder] ?? [] : [],
  );
  let activeFolderIsRepo = $derived(
    route.folder ? folderIsRepo[route.folder] === true : false,
  );

  // Folder-management modal state. AddFolderDialog owns its own
  // form; rename/remove are local because they only need a tiny
  // payload (new name / confirm). Keeping the editing target on the
  // workspace lets the dropdown stay open while the dialog runs.
  let addFolderOpen = $state(false);
  let renameFolderTarget = $state<Folder | null>(null);
  let renameFolderValue = $state("");
  let renameFolderError: string | null = $state(null);
  let renameFolderSaving = $state(false);
  let removeFolderTarget = $state<Folder | null>(null);
  let removeFolderError: string | null = $state(null);
  let removingFolder = $state(false);

  // File-op modal state. Only one is open at a time; each carries its
  // own input value + error message so submit can show "name in use",
  // "not allowed", etc. inline.
  let newFileOpen = $state(false);
  let newFileName = $state("");
  let newFileError: string | null = $state(null);
  let newFileSaving = $state(false);

  let renameOpen = $state(false);
  let renameName = $state("");
  let renameError: string | null = $state(null);
  let renameSaving = $state(false);

  let deleteOpen = $state(false);
  let deleteError: string | null = $state(null);
  let deleting = $state(false);

  let fileMenuOpen = $state(false);
  let fileMenuRoot: HTMLDivElement | null = $state(null);

  function isMarkdownName(name: string): boolean {
    return /\.(md|markdown)$/i.test(name);
  }

  function ensureMarkdownExt(name: string): string {
    const trimmed = name.trim();
    if (!trimmed) return "";
    return isMarkdownName(trimmed) ? trimmed : `${trimmed}.md`;
  }

  function openNewFileModal() {
    newFileName = "";
    newFileError = null;
    newFileOpen = true;
  }

  function openRenameModal() {
    if (!route.doc) return;
    fileMenuOpen = false;
    renameName = route.doc.split("/").pop() ?? route.doc;
    renameError = null;
    renameOpen = true;
  }

  function openDeleteModal() {
    if (!route.doc) return;
    fileMenuOpen = false;
    deleteError = null;
    deleteOpen = true;
  }

  function submitNewFile(): void {
    const folderID = route.folder;
    if (!folderID || newFileSaving) return;
    if (editing && editorDirty) {
      newFileError = "Save or cancel the open edit before creating a new file.";
      return;
    }
    const name = ensureMarkdownExt(newFileName);
    if (!name) {
      newFileError = "Enter a filename.";
      return;
    }
    if (name.includes("/")) {
      newFileError = "Subfolders aren't supported yet — name only.";
      return;
    }
    newFileSaving = true;
    newFileError = null;
    const requestedAPI = api;
    runPresentedCommand(
      queueDocsMutation(
        Effect.gen(function* () {
          const workflow = yield* DocsWorkflow;
          const outcome = yield* workflow.reconcileMutation(
            {
              resource: docsResourceKey("tree", folderID),
              intent: docsIntentKey("create", folderID, name),
            },
            "create Docs document",
            executeDocsRequest("create Docs document", (signal) =>
              requestedAPI.createFile(folderID, name, "", signal),
            ),
            readTree(folderID, requestedAPI, docsMutationOwner),
            (snapshot) => (flattenTreePaths(snapshot).includes(name) ? Option.some(undefined) : Option.none()),
          );
          return { value: outcome.snapshot, error: null } satisfies TreeReconciliation;
        }),
      ),
      {
        operation: "create Docs document",
        safeContext: { folderID, name },
        onFailure: (failure) => {
          const message = describeFileError(failure, "Could not create file.");
          if (isFileInputError(failure)) newFileError = message;
          else showFlash(message, { tone: "danger" });
        },
        onSuccess: (reconciliation) => {
          if (reconciliation !== null) applyTreeReconciliation(folderID, reconciliation);
          newFileOpen = false;
          if (route.folder === folderID) {
            onRouteChange({ mode: "docs", folder: folderID, doc: name });
          }
        },
        onDone: () => {
          newFileSaving = false;
        },
      },
    );
  }

  // Right-click → Rename in the file tree resolves here. The from/to
  // paths come straight from @pierre/trees' inline rename input, so
  // they're already scoped to the active folder and may target any
  // file in the tree (not just route.doc). The tree library invokes
  // this callback synchronously, so the application runtime owns the
  // request, failure presentation, and canonical tree reconciliation.
  function handleInlineRename(from: string, to: string): void {
    const folderID = route.folder;
    if (!folderID) return;
    if (from === to) return;
    inlineFileError = null;
    const requestedAPI = api;
    const dirtyEditorError =
      editing && editorDirty && from === route.doc
        ? new Error("Save or cancel the open edit before renaming.")
        : null;
    if (dirtyEditorError !== null) {
      inlineFileError = dirtyEditorError.message;
      return;
    }
    runPresentedCommand(
      queueDocsMutation(
        Effect.gen(function* () {
          const workflow = yield* DocsWorkflow;
          const outcome = yield* workflow.reconcileMutation(
            {
              resource: docsResourceKey("tree", folderID),
              intent: docsIntentKey("rename", folderID, from, to),
            },
            "rename Docs document",
            executeDocsRequest("rename Docs document", (signal) =>
              requestedAPI.renameFile(folderID, from, to, signal),
            ),
            readTree(folderID, requestedAPI, docsMutationOwner),
            (snapshot) => {
              const paths = flattenTreePaths(snapshot);
              return paths.includes(to) && !paths.includes(from) ? Option.some(undefined) : Option.none();
            },
          );
          return { value: outcome.snapshot, error: null } satisfies TreeReconciliation;
        }),
      ),
      {
        operation: "rename Docs document inline",
        safeContext: { folderID, from, to },
        onFailure: (failure) => {
          inlineFileError = describeFileError(failure, "Could not rename file.");
        },
        onSuccess: (reconciliation) => {
          applyTreeReconciliation(folderID, reconciliation);
          if (route.folder === folderID && route.doc === from) {
            onRouteChange({ mode: "docs", folder: folderID, doc: to }, { replace: true });
          }
        },
      },
    );
  }

  function submitRename(): void {
    const folderID = route.folder;
    const docPath = route.doc;
    if (!folderID || !docPath || renameSaving) return;
    if (editing && editorDirty) {
      renameError = "Save or cancel the open edit before renaming.";
      return;
    }
    const target = ensureMarkdownExt(renameName);
    if (!target) {
      renameError = "Enter a filename.";
      return;
    }
    if (target.includes("/")) {
      renameError = "Rename within the same folder — name only.";
      return;
    }
    const parent = docPath.includes("/")
      ? docPath.slice(0, docPath.lastIndexOf("/") + 1)
      : "";
    const newPath = `${parent}${target}`;
    if (newPath === docPath) {
      renameOpen = false;
      return;
    }
    renameSaving = true;
    renameError = null;
    const requestedAPI = api;
    runPresentedCommand(
      queueDocsMutation(
        Effect.gen(function* () {
          const workflow = yield* DocsWorkflow;
          const outcome = yield* workflow.reconcileMutation(
            {
              resource: docsResourceKey("tree", folderID),
              intent: docsIntentKey("rename", folderID, docPath, newPath),
            },
            "rename Docs document",
            executeDocsRequest("rename Docs document", (signal) =>
              requestedAPI.renameFile(folderID, docPath, newPath, signal),
            ),
            readTree(folderID, requestedAPI, docsMutationOwner),
            (snapshot) => {
              const paths = flattenTreePaths(snapshot);
              return paths.includes(newPath) && !paths.includes(docPath) ? Option.some(undefined) : Option.none();
            },
          );
          return { value: outcome.snapshot, error: null } satisfies TreeReconciliation;
        }),
      ),
      {
        operation: "rename Docs document",
        safeContext: { folderID, from: docPath, to: newPath },
        onFailure: (failure) => {
          const message = describeFileError(failure, "Could not rename file.");
          if (isFileInputError(failure)) renameError = message;
          else showFlash(message, { tone: "danger" });
        },
        onSuccess: (reconciliation) => {
          applyTreeReconciliation(folderID, reconciliation);
          renameOpen = false;
          if (route.folder === folderID && route.doc === docPath) {
            onRouteChange({ mode: "docs", folder: folderID, doc: newPath }, { replace: true });
          }
        },
        onDone: () => {
          renameSaving = false;
        },
      },
    );
  }

  function submitDelete(): void {
    const folderID = route.folder;
    const docPath = route.doc;
    if (!folderID || !docPath || deleting) return;
    if (editing && editorDirty) {
      deleteError = "Save or cancel the open edit before deleting.";
      return;
    }
    deleting = true;
    deleteError = null;
    const requestedAPI = api;
    runPresentedCommand(
      queueDocsMutation(
        Effect.gen(function* () {
          const workflow = yield* DocsWorkflow;
          const outcome = yield* workflow.reconcileMutation(
            {
              resource: docsResourceKey("tree", folderID),
              intent: docsIntentKey("delete", folderID, docPath),
            },
            "delete Docs document",
            executeDocsRequest("delete Docs document", (signal) =>
              requestedAPI.deleteFile(folderID, docPath, signal),
            ),
            readTree(folderID, requestedAPI, docsMutationOwner),
            (snapshot) =>
              flattenTreePaths(snapshot).includes(docPath) ? Option.none() : Option.some(undefined),
          );
          return { value: outcome.snapshot, error: null } satisfies TreeReconciliation;
        }),
      ),
      {
        operation: "delete Docs document",
        safeContext: { folderID, docPath },
        onFailure: (failure) => {
          const message = describeFileError(failure, "Could not delete file.");
          if (isFileInputError(failure)) deleteError = message;
          else showFlash(message, { tone: "danger" });
        },
        onSuccess: (reconciliation) => {
          applyTreeReconciliation(folderID, reconciliation);
          deleteOpen = false;
          if (route.folder === folderID && route.doc === docPath) {
            onRouteChange({ mode: "docs", folder: folderID, doc: null }, { replace: true });
          }
        },
        onDone: () => {
          deleting = false;
        },
      },
    );
  }

  function describeFileError(err: unknown, fallback: string): string {
    if (err instanceof DocsMutationStateUncertainError) {
      return `${fallback} Saved state could not be confirmed. Reload Docs before retrying.`;
    }
    if (err instanceof DocsRequestError) {
      if (err.code === "already_exists") return "A file with that name already exists.";
      if (err.code === "unsupported_extension") return "Only .md files are supported.";
      if (err.code === "outside_folder") return "That path isn't allowed.";
      return err.message || fallback;
    }
    if (err instanceof Error) return err.message || fallback;
    return fallback;
  }

  function isFileInputError(err: unknown): boolean {
    return (
      err instanceof DocsRequestError &&
      (err.code === "already_exists" || err.code === "unsupported_extension" || err.code === "outside_folder")
    );
  }

  function toggleFileMenu() {
    fileMenuOpen = !fileMenuOpen;
  }

  $effect(() => {
    if (!fileMenuOpen) return;
    function onPointerDown(event: PointerEvent) {
      if (!fileMenuRoot) return;
      if (event.target instanceof Node && fileMenuRoot.contains(event.target)) return;
      fileMenuOpen = false;
    }
    window.addEventListener("pointerdown", onPointerDown, true);
    return () => window.removeEventListener("pointerdown", onPointerDown, true);
  });

  function openAddFolder() {
    addFolderOpen = true;
  }

  function handleFolderAdded(folder: Folder): void {
    addFolderOpen = false;
    loadFolders();
    onRouteChange({ mode: "docs", folder: folder.id, doc: null });
  }

  function openRenameFolder(folder: Folder) {
    renameFolderTarget = folder;
    renameFolderValue = folder.name;
    renameFolderError = null;
  }

  function submitRenameFolder(): void {
    if (!renameFolderTarget || renameFolderSaving) return;
    const target = renameFolderTarget;
    const next = renameFolderValue.trim();
    if (!next) {
      renameFolderError = "Name can't be empty.";
      return;
    }
    if (next === target.name) {
      renameFolderTarget = null;
      return;
    }
    renameFolderSaving = true;
    renameFolderError = null;
    const requestedAPI = api;
    runPresentedCommand(
      queueDocsMutation(
        Effect.gen(function* () {
          const workflow = yield* DocsWorkflow;
          return yield* workflow.reconcileMutation(
            {
              resource: docsResourceKey("folders"),
              intent: docsIntentKey("rename-folder", target.id, next),
            },
            "rename Docs folder",
            executeDocsRequest("rename Docs folder", (signal) => requestedAPI.renameFolder(target.id, next, signal)),
            readFolders(requestedAPI, docsMutationOwner),
            (snapshot) => {
              const recovered = snapshot.find((folder) => folder.id === target.id && folder.name === next);
              return recovered === undefined ? Option.none() : Option.some(recovered);
            },
          );
        }),
      ),
      {
        operation: "rename Docs folder",
        safeContext: { folderID: target.id },
        onFailure: (failure) => {
          showFlash(describeFileError(failure, "Could not rename folder."), { tone: "danger" });
        },
        onSuccess: (outcome) => {
          folders = outcome.snapshot;
          if (renameFolderTarget?.id === target.id) renameFolderTarget = null;
        },
        onDone: () => {
          renameFolderSaving = false;
        },
      },
    );
  }

  function openRemoveFolder(folder: Folder) {
    removeFolderTarget = folder;
    removeFolderError = null;
  }

  function submitRemoveFolder(): void {
    if (!removeFolderTarget || removingFolder) return;
    const target = removeFolderTarget;
    // Block removal of the currently-viewed folder while a dirty
    // edit is open. Letting the route switch away would unmount the
    // editor and a later save would target the deleted folder,
    // matching the guard the create/rename/delete-file flows already use.
    if (target.id === route.folder && editing && editorDirty) {
      removeFolderError =
        "Save or cancel the open edit before removing this folder.";
      return;
    }
    removingFolder = true;
    removeFolderError = null;
    const requestedAPI = api;
    runPresentedCommand(
      queueDocsMutation(
        Effect.gen(function* () {
          const workflow = yield* DocsWorkflow;
          return yield* workflow.reconcileMutation(
            {
              resource: docsResourceKey("folders"),
              intent: docsIntentKey("remove-folder", target.id),
            },
            "remove Docs folder",
            executeDocsRequest("remove Docs folder", (signal) => requestedAPI.removeFolder(target.id, signal)),
            readFolders(requestedAPI, docsMutationOwner),
            (snapshot) =>
              snapshot.some((folder) => folder.id === target.id) ? Option.none() : Option.some(undefined),
          );
        }),
      ),
      {
        operation: "remove Docs folder",
        safeContext: { folderID: target.id },
        onFailure: (failure) => {
          showFlash(describeFileError(failure, "Could not remove folder."), { tone: "danger" });
        },
        onSuccess: (outcome) => {
          const remaining = outcome.snapshot;
          folders = remaining;
          if (removeFolderTarget?.id === target.id) removeFolderTarget = null;
          if (route.folder === target.id) {
            autoOpenedFor = null;
            const fallback = remaining[0]?.id ?? null;
            onRouteChange({ mode: "docs", folder: fallback, doc: null });
          }
        },
        onDone: () => {
          removingFolder = false;
        },
      },
    );
  }

  function onPublishedSuccess(result: GitPublishResponse): void {
    gitNotice = {
      kind: "success",
      text: `Committed and pushed ${result.files.length} ${result.files.length === 1 ? "file" : "files"} as ${result.short_commit}.`,
    };
    if (route.folder) loadGitStatus(route.folder);
  }

  function pullFromGit(): void {
    const folderID = route.folder;
    if (!folderID || pulling) return;
    pulling = true;
    // Pull reconciliation belongs to the accepted mutation, not the visible
    // folder. Exact resource identities keep these reads alive if the user
    // navigates away, while the presenter lease prevents their result from
    // being published over the replacement view.
    const requestedAPI = api;
    const docPath = route.doc && !editing ? route.doc : null;
    runPresentedCommand(
      queueDocsMutation(
        Effect.gen(function* () {
          const confirmation = yield* retryIdempotentDocsRequest(
            executeDocsRequest("pull Docs folder", (signal) => requestedAPI.gitPull(folderID, signal)).pipe(
              Effect.map((result): PullConfirmation => ({ kind: "response", result })),
            ),
          );
          const [treeSnapshot, gitStatusSnapshot, documentSnapshot] = yield* Effect.all(
            [
              reconcileTree(folderID, requestedAPI),
              reconcileGitStatus(folderID, requestedAPI),
              docPath === null
                ? Effect.succeed(null)
                : reconcileDocument(folderID, docPath, requestedAPI),
            ],
            { concurrency: "unbounded" },
          );
          return {
            confirmation,
            tree: treeSnapshot,
            gitStatus: gitStatusSnapshot,
            document: documentSnapshot,
          } satisfies PullReconciliation;
        }),
      ),
      {
        operation: "pull Docs folder",
        safeContext: { folderID },
        onFailure: (failure) => {
          if (route.folder === folderID) {
            gitNotice = { kind: "error", text: describeFileError(failure, "Pull failed") };
          }
        },
        onSuccess: (reconciliation) => {
          if (reconciliation.tree !== null) applyTreeReconciliation(folderID, reconciliation.tree);
          if (reconciliation.gitStatus !== null) {
            applyGitStatusReconciliation(folderID, reconciliation.gitStatus);
          }
          if (reconciliation.document !== null) {
            applyDocumentReconciliation(folderID, reconciliation.document);
          }
          if (route.folder !== folderID) return;
          const degradedRefreshes = [
            reconciliation.tree?.error === null ? null : "tree",
            reconciliation.gitStatus?.error === null ? null : "Git status",
            reconciliation.document === null || reconciliation.document.error === null ? null : "open document",
          ].filter((label): label is string => label !== null);
          const refreshNotice =
            degradedRefreshes.length === 0
              ? ""
              : ` Some views could not be refreshed: ${degradedRefreshes.join(", ")}.`;
          gitNotice = {
            kind: "success",
            text: reconciliation.confirmation.result.up_to_date
              ? `Already up to date.${refreshNotice}`
              : `Pulled to ${reconciliation.confirmation.result.short_commit}.${refreshNotice}`,
          };
        },
        onDone: () => {
          pulling = false;
        },
      },
    );
  }
</script>

<div class="docs-workspace" bind:this={workspaceRoot}>
  <div class="docs-list">
    <div class="list-header">
      {#if folders.length > 0}
        <SelectDropdown
          class="folder-select"
          title="Switch folder"
          value={route.folder ?? folders[0]?.id ?? ""}
          options={folderOptions}
          disabled={loadingFolders}
          onchange={selectFolder}
        />
      {:else}
        <span
          class:folder-status--error={Boolean(foldersError)}
          class="folder-status"
          role="status"
        >
          {foldersError ?? (loadingFolders ? "Loading folders…" : "No folders configured.")}
        </span>
      {/if}
      <div class="folder-actions">
        <IconButton size="sm" ariaLabel="Add folder" onclick={openAddFolder} disabled={loadingFolders}>
          <Plus size={14} strokeWidth={2} />
        </IconButton>
        {#if route.folder}
          <IconButton size="sm" ariaLabel="New file" onclick={openNewFileModal}>
            <FileText size={13} strokeWidth={1.75} />
          </IconButton>
          {#if activeFolderIsRepo}
            <!-- Disabled while the editor is open: a pull that rewrites the
                 open document would recreate the editor and silently discard
                 an unsaved draft. -->
            <IconButton
              size="sm"
              ariaLabel="Pull from git"
              title="Pull from git"
              onclick={pullFromGit}
              disabled={pulling || editing}
            >
              <Download size={14} strokeWidth={1.75} />
            </IconButton>
            <IconButton
              size="sm"
              ariaLabel="Publish to git"
              title="Commit & push to git"
              onclick={() => (publishOpen = true)}
            >
              <Upload size={14} strokeWidth={1.75} />
            </IconButton>
          {/if}
          {#if activeFolder}
            <IconButton
              size="sm"
              ariaLabel={`Rename ${activeFolder.name}`}
              title="Rename folder"
              onclick={() => openRenameFolder(activeFolder!)}
            >
              <Pencil size={12} strokeWidth={1.75} />
            </IconButton>
            <IconButton
              size="sm"
              tone="danger"
              ariaLabel={`Remove ${activeFolder.name}`}
              title="Remove folder"
              onclick={() => openRemoveFolder(activeFolder!)}
            >
              <Trash2 size={12} strokeWidth={1.75} />
            </IconButton>
          {/if}
        {/if}
      </div>
    </div>

    <div class="list-body">
      {#if staleFolderDaemon}
        <div class="folder-daemon-warning" role="status">
          Daemon {staleFolderDaemon} is not available. Task links cannot be opened.
        </div>
      {/if}
      {#if !route.folder}
        {#if foldersError}
          <p class="error placeholder">{foldersError}</p>
        {:else if !loadingFolders && folders.length === 0}
          <p class="muted placeholder">No folders configured. Add one to get started.</p>
        {:else}
          <p class="muted placeholder">Pick a folder to browse.</p>
        {/if}
      {:else if treeError}
        <p class="error placeholder">{treeError}</p>
      {:else if loadingTree && !tree}
        <p class="muted placeholder">Loading tree…</p>
      {:else}
        {#if inlineFileError}
          <div class="inline-rename-error" role="alert">
            {inlineFileError}
            <button
              type="button"
              class="inline-rename-dismiss"
              aria-label="Dismiss"
              onclick={() => (inlineFileError = null)}
            >×</button>
          </div>
        {/if}
        <FolderTree
          {tree}
          gitEntries={activeFolderGit}
          activePath={route.doc}
          onSelect={(path) => path && selectDoc(path)}
          onFileRename={handleInlineRename}
        />
      {/if}
    </div>
  </div>

  <section class="docs-detail" aria-label="Document">
    {#if !route.folder}
      <div class="empty">
        <FileText size={32} strokeWidth={1.5} />
        {#if foldersError}
          <p class="error">{foldersError}</p>
        {:else if !loadingFolders && folders.length === 0}
          <p>No folders yet — add one from the sidebar to start writing.</p>
        {:else}
          <p>Pick a folder to get started.</p>
        {/if}
      </div>
    {:else if !route.doc}
      <div class="empty">
        <FileText size={32} strokeWidth={1.5} />
        <p>Select a document</p>
      </div>
    {:else if docError}
      <div class="empty">
        <p class="error">{docError}</p>
      </div>
    {:else if loadingDoc && docContent === null}
      <div class="empty">
        <p class="muted">Loading…</p>
      </div>
    {:else if docContent !== null && route.folder && route.doc}
      <article class="doc-pane" class:doc-pane--outline-collapsed={outlineCollapsed}>
        <header class="doc-toolbar">
          <div class="doc-path" title={route.doc}>
            {route.doc}{editorDirty ? " *" : ""}
          </div>
          <div class="doc-actions">
            {#if saveError}
              <span class="save-error" role="status">{saveError}</span>
            {/if}
            {#if editing}
              <button
                type="button"
                class="toolbar-btn"
                onclick={cancelEdit}
                disabled={saving}
              >Cancel</button>
              <button
                type="button"
                class="toolbar-btn primary"
                onclick={() => saveEdit(editorDraft)}
                disabled={saving || !editorDirty}
              >{saving ? "Saving…" : "Save"}</button>
            {:else}
              <button
                type="button"
                class="toolbar-btn"
                onclick={beginEdit}
                disabled={!editReady || editorLoading || pulling}
              >{editorLoading ? "Loading…" : "Edit"}</button>
              <button
                type="button"
                class="toolbar-btn toolbar-btn--icon"
                aria-label={outlineCollapsed ? "Show outline" : "Hide outline"}
                aria-pressed={!outlineCollapsed}
                title={outlineCollapsed ? "Show outline (])" : "Hide outline (])"}
                onclick={toggleOutline}
              >
                {#if outlineCollapsed}
                  <PanelRight size={14} strokeWidth={1.75} />
                {:else}
                  <PanelRightClose size={14} strokeWidth={1.75} />
                {/if}
              </button>
            {/if}
            <div
              class="file-menu-host"
              bind:this={fileMenuRoot}
              role="presentation"
            >
              <button
                type="button"
                class="toolbar-btn toolbar-btn--icon"
                aria-label="File actions"
                aria-haspopup="menu"
                aria-expanded={fileMenuOpen}
                onclick={toggleFileMenu}
              >
                <MoreHorizontal size={14} strokeWidth={2} />
              </button>
              {#if fileMenuOpen}
                <ul class="file-menu kit-popover-card" role="menu" aria-label="File actions">
                  <li>
                    <button
                      type="button"
                      class="file-menu-item"
                      role="menuitem"
                      onclick={openRenameModal}
                    >Rename…</button>
                  </li>
                  <li>
                    <button
                      type="button"
                      class="file-menu-item file-menu-item--danger"
                      role="menuitem"
                      onclick={openDeleteModal}
                    >Delete…</button>
                  </li>
                </ul>
              {/if}
            </div>
          </div>
        </header>
        {#if editing}
          <div class="doc-edit">
            {#if DocMarkdownEditor}
              <DocMarkdownEditor
                initialValue={docContent}
                onChange={handleEditorChange}
                onSave={saveEdit}
                onCancel={cancelEdit}
                completionSources={[issueCompletionSource, wikilinkCompletionSource]}
              />
            {:else}
              <div class="editor-load-state" role="status">
                {#if editorLoadError}
                  <span>Editor failed to load.</span>
                  <button type="button" class="toolbar-btn" onclick={() => loadEditor()}>Retry</button>
                {:else}
                  <span>Loading editor…</span>
                {/if}
              </div>
            {/if}
          </div>
        {:else}
          <div class="doc-scroll">
            <DocMarkdownView
              source={docContent}
              options={{
                folderID: route.folder,
                currentDocPath: route.doc,
                index: folderIndex,
                buildDocURL,
                buildBlobURL,
              }}
              onState={handleMarkdownState}
              onSelectDoc={selectDoc}
              onSelectKataReference={handleKataReference}
              scrollToAnchor={pendingAnchor}
              onAnchorConsumed={() => (pendingAnchor = null)}
            />
          </div>
          <DocOutline {headings} activeId={activeHeadingID} onSelect={selectHeading} />
        {/if}
      </article>
    {/if}
  </section>
</div>

<Modal
  open={newFileOpen}
  title="New file"
  width={520}
  onClose={() => (newFileOpen = false)}
>
  <form
    class="modal-form"
    onsubmit={(event) => {
      event.preventDefault();
      submitNewFile();
    }}
  >
    <label class="modal-field">
      <span>Filename</span>
      <input
        type="text"
        bind:value={newFileName}
        placeholder="Untitled.md"
        disabled={newFileSaving}
      />
    </label>
    <p class="modal-hint">.md will be added if missing. Top-level only for now.</p>
    {#if newFileError}
      <p class="modal-error" role="alert">{newFileError}</p>
    {/if}
    <div class="modal-actions">
      <button type="button" class="toolbar-btn" onclick={() => (newFileOpen = false)} disabled={newFileSaving}>
        Cancel
      </button>
      <button type="submit" class="toolbar-btn primary" disabled={newFileSaving}>
        {newFileSaving ? "Creating…" : "Create"}
      </button>
    </div>
  </form>
</Modal>

<Modal
  open={renameOpen}
  title="Rename file"
  width={520}
  onClose={() => (renameOpen = false)}
>
  <form
    class="modal-form"
    onsubmit={(event) => {
      event.preventDefault();
      submitRename();
    }}
  >
    <label class="modal-field">
      <span>New name</span>
      <input
        type="text"
        bind:value={renameName}
        disabled={renameSaving}
      />
    </label>
    <p class="modal-hint">Renames within the current folder.</p>
    {#if renameError}
      <p class="modal-error" role="alert">{renameError}</p>
    {/if}
    <div class="modal-actions">
      <button type="button" class="toolbar-btn" onclick={() => (renameOpen = false)} disabled={renameSaving}>
        Cancel
      </button>
      <button type="submit" class="toolbar-btn primary" disabled={renameSaving}>
        {renameSaving ? "Renaming…" : "Rename"}
      </button>
    </div>
  </form>
</Modal>

<Modal
  open={deleteOpen}
  title="Delete file"
  width={520}
  onClose={() => (deleteOpen = false)}
>
  <p class="modal-body-text">
    Delete <code>{route.doc}</code>? This can't be undone from the app.
  </p>
  {#if deleteError}
    <p class="modal-error" role="alert">{deleteError}</p>
  {/if}
  <div class="modal-actions">
    <button type="button" class="toolbar-btn" onclick={() => (deleteOpen = false)} disabled={deleting}>
      Cancel
    </button>
    <button
      type="button"
      class="toolbar-btn danger"
      onclick={submitDelete}
      disabled={deleting}
    >
      {deleting ? "Deleting…" : "Delete"}
    </button>
  </div>
</Modal>

<AddFolderDialog
  open={addFolderOpen}
  {api}
  presentationSurfaceID={docsPresentationSurface}
  presentationSessionID={docsOwner}
  daemonRoster={kataDaemons.daemons().map((daemon) => daemon.id)}
  daemonRosterLoaded={kataDaemons.loaded()}
  onClose={() => (addFolderOpen = false)}
  onAdded={handleFolderAdded}
/>

{#if route.folder}
  <PublishDocsDialog
    open={publishOpen}
    folderID={route.folder}
    {api}
    onClose={() => (publishOpen = false)}
    onPublished={onPublishedSuccess}
  />
{/if}

{#if gitNotice}
  <p
    class="publish-success kit-popover-card"
    class:notice-error={gitNotice.kind === "error"}
    role="status"
  >
    {gitNotice.text}
  </p>
{/if}

<Modal
  open={renameFolderTarget !== null}
  title="Rename folder"
  width={520}
  onClose={() => (renameFolderTarget = null)}
>
  <form
    class="modal-form"
    onsubmit={(event) => {
      event.preventDefault();
      submitRenameFolder();
    }}
  >
    <label class="modal-field">
      <span>Name</span>
      <input
        type="text"
        bind:value={renameFolderValue}
        disabled={renameFolderSaving}
      />
    </label>
    {#if renameFolderError}
      <p class="modal-error" role="alert">{renameFolderError}</p>
    {/if}
    <div class="modal-actions">
      <button
        type="button"
        class="toolbar-btn"
        onclick={() => (renameFolderTarget = null)}
        disabled={renameFolderSaving}
      >Cancel</button>
      <button type="submit" class="toolbar-btn primary" disabled={renameFolderSaving}>
        {renameFolderSaving ? "Renaming…" : "Rename"}
      </button>
    </div>
  </form>
</Modal>

<Modal
  open={removeFolderTarget !== null}
  title="Remove folder"
  width={520}
  onClose={() => (removeFolderTarget = null)}
>
  <p class="modal-body-text">
    Remove <strong>{removeFolderTarget?.name}</strong> from kenn-forge? The
    folder on disk stays put — only the registration is dropped.
  </p>
  {#if removeFolderError}
    <p class="modal-error" role="alert">{removeFolderError}</p>
  {/if}
  <div class="modal-actions">
    <button
      type="button"
      class="toolbar-btn"
      onclick={() => (removeFolderTarget = null)}
      disabled={removingFolder}
    >Cancel</button>
    <button
      type="button"
      class="toolbar-btn danger"
      onclick={submitRemoveFolder}
      disabled={removingFolder}
    >
      {removingFolder ? "Removing…" : "Remove"}
    </button>
  </div>
</Modal>

<style>
  .docs-workspace {
    flex: 1;
    min-height: 0;
    display: grid;
    grid-template-columns: 280px minmax(360px, 1fr);
    overflow: hidden;
  }

  .docs-list {
    display: flex;
    flex-direction: column;
    overflow: hidden;
    border-right: 1px solid var(--border-default);
    background: var(--bg-primary);
  }

  .list-header {
    position: relative;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 10px;
    border-bottom: 1px solid var(--border-muted);
  }

  :global(.folder-select) {
    flex: 1 1 auto;
    min-width: 0;
  }

  .folder-actions {
    display: flex;
    flex: 0 0 auto;
    align-items: center;
    gap: var(--space-1);
  }

  .folder-status {
    flex: 1 1 auto;
    min-width: 0;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .folder-status--error {
    color: var(--accent-red);
  }

  .list-body {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .folder-daemon-warning {
    margin: 6px 8px 0;
    padding: 6px 8px;
    border: 1px solid color-mix(in srgb, var(--accent-amber) 45%, var(--border-muted));
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--accent-amber) 10%, transparent);
    color: var(--text-secondary);
    font-size: var(--font-size-xs);
    line-height: 1.35;
  }

  .placeholder {
    padding: 8px 12px;
  }

  .muted {
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .error {
    color: var(--accent-red, #c14a3c);
    font-size: var(--font-size-sm);
    padding: 8px 12px;
  }

  .inline-rename-error {
    margin: 6px 8px 4px;
    padding: 6px 8px;
    background: color-mix(in srgb, var(--accent-red) 8%, transparent);
    border: 1px solid var(--accent-red, #c14a3c);
    border-radius: var(--radius-sm);
    color: var(--accent-red, #c14a3c);
    font-size: var(--font-size-xs);
    display: flex;
    align-items: flex-start;
    gap: 6px;
  }

  .inline-rename-dismiss {
    margin-left: auto;
    background: transparent;
    border: none;
    color: inherit;
    cursor: pointer;
    font-size: var(--font-size-md);
    line-height: 1;
    padding: 0 2px;
  }

  .docs-detail {
    display: flex;
    background: var(--bg-surface);
    overflow: hidden;
    min-width: 0;
    min-height: 0;
  }

  .doc-pane {
    flex: 1;
    display: grid;
    grid-template-rows: auto minmax(0, 1fr);
    grid-template-columns: minmax(0, 1fr) 220px;
    grid-template-areas:
      "toolbar toolbar"
      "body outline";
    overflow: hidden;
    min-width: 0;
    min-height: 0;
  }

  /* Collapse the outline column away when the user has hidden the TOC
     so the body reclaims the width. The DocOutline element is hidden
     via the :global() rule below — keeping it mounted preserves the
     intersection observer that tracks the active heading. */
  .doc-pane--outline-collapsed {
    grid-template-columns: minmax(0, 1fr);
    grid-template-areas:
      "toolbar"
      "body";
  }
  .doc-pane--outline-collapsed :global(.doc-outline) {
    display: none;
  }

  .doc-toolbar {
    grid-area: toolbar;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 8px 16px;
    border-bottom: 1px solid var(--border-hairline);
    background: var(--bg-surface);
    min-height: 36px;
  }

  .doc-path {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: var(--font-size-sm);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .doc-actions {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .toolbar-btn {
    padding: 4px 10px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    cursor: pointer;
  }

  .toolbar-btn:hover:not(:disabled) {
    background: var(--bg-surface-hover);
  }

  .toolbar-btn:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }

  .toolbar-btn.primary {
    background: var(--accent-blue);
    border-color: var(--accent-blue);
    color: white;
  }

  .toolbar-btn.primary:hover:not(:disabled) {
    background: var(--accent-blue);
    filter: brightness(1.08);
  }

  .toolbar-btn--icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 4px;
    width: 26px;
    height: 26px;
  }

  .toolbar-btn.danger {
    background: var(--accent-red, #c14a3c);
    border-color: var(--accent-red, #c14a3c);
    color: white;
  }

  .toolbar-btn.danger:hover:not(:disabled) {
    filter: brightness(1.08);
  }

  .file-menu-host {
    position: relative;
  }

  .file-menu {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 30;
    list-style: none;
    margin: 0;
    padding: 4px;
    min-width: 160px;
  }

  .file-menu-item {
    width: 100%;
    display: block;
    padding: 6px 10px;
    text-align: left;
    background: transparent;
    border: none;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    border-radius: var(--radius-sm);
    cursor: pointer;
  }

  .file-menu-item:hover {
    background: var(--bg-surface-hover);
  }

  .file-menu-item--danger {
    color: var(--accent-red, #c14a3c);
  }

  .modal-form {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
  }

  .modal-field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
  }

  .modal-field input {
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-default);
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: var(--font-size-md);
    font-family: var(--font-mono);
  }

  .modal-field input:focus {
    outline: 2px solid var(--accent-blue);
    outline-offset: -1px;
  }

  .modal-hint {
    margin: 0;
    font-size: var(--font-size-xs);
    color: var(--text-muted);
  }

  .modal-error {
    margin: 0;
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--accent-red) 10%, transparent);
    color: var(--accent-red, #c14a3c);
    font-size: var(--font-size-sm);
  }

  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding-top: 4px;
  }

  .modal-body-text {
    margin: 0 0 10px;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .modal-body-text code {
    font-family: var(--font-mono);
    color: var(--accent-blue);
  }

  .save-error {
    color: var(--accent-red);
    font-size: var(--font-size-sm);
  }

  .publish-success {
    position: fixed;
    bottom: 16px;
    right: 16px;
    z-index: 50;
    padding: 8px 14px;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
  }

  .publish-success.notice-error {
    color: var(--accent-red);
  }

  .doc-scroll {
    grid-area: body;
    overflow: auto;
    padding: 24px 36px 80px;
    max-width: 760px;
    margin: 0 auto;
    width: 100%;
  }

  .doc-edit {
    grid-column: 1 / -1;
    overflow: hidden;
    padding: 12px;
    min-height: 0;
  }

  .editor-load-state {
    height: 100%;
    min-height: 160px;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  .empty {
    margin: auto;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-4);
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  @media (max-width: 900px) {
    .doc-pane {
      grid-template-columns: minmax(0, 1fr);
    }
    .doc-pane :global(.doc-outline) {
      display: none;
    }
  }
</style>
