<script lang="ts">
  import Check from "@lucide/svelte/icons/check";
  import ChevronDown from "@lucide/svelte/icons/chevron-down";
  import FileText from "@lucide/svelte/icons/file-text";
  import FolderIcon from "@lucide/svelte/icons/folder";
  import MoreHorizontal from "@lucide/svelte/icons/more-horizontal";
  import PanelRight from "@lucide/svelte/icons/panel-right";
  import PanelRightClose from "@lucide/svelte/icons/panel-right-close";
  import Pencil from "@lucide/svelte/icons/pencil";
  import Plus from "@lucide/svelte/icons/plus";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import Upload from "@lucide/svelte/icons/upload";
  import type { DocsRoute } from "../../api/docs/route.js";
  import { createDocsAPI, type DocsAPI } from "../../api/docs/api";
  import type { DocsAPIError, GitPublishResponse, GitStatusEntry, TreeNode, Folder } from "../../api/docs/types";
  import PublishDocsDialog from "./PublishDocsDialog.svelte";
  import { buildFolderIndex, type FolderIndex } from "../../api/docs/folderLinks";
  import { docsHref } from "../../api/docs/route.js";
  import { withBasePath } from "../../stores/router.svelte.js";
  import { untrack } from "svelte";
  import {
    getActiveKataDaemon,
    getKataDaemonRoster,
    getKataDaemonRosterLoaded,
  } from "../../stores/active-kata-daemon.svelte";
  import DocMarkdownView, { type DocMarkdownState, type HeadingEntry } from "./DocMarkdownView.svelte";
  import DocOutline from "./DocOutline.svelte";
  import FolderTree from "./FolderTree.svelte";
  import Modal from "./DocsModal.svelte";
  import AddFolderDialog from "./AddFolderDialog.svelte";
  import { buildIssueCompletionSource } from "./issueCompletion";
  import { buildDocsIssueCompletionOptions } from "./docsIssueCompletionOptions";
  import { effectiveDocsFolderDaemon } from "./folderDaemon";
  import { buildMentionCompletionSource, collectMentionNames } from "./mentionCompletion";
  import { buildWikilinkCompletionSource } from "./wikilinkCompletion";
  import type { IssueSummary, KataAPI } from "./docsIssueTypes";

  interface Props {
    route: DocsRoute;
    onRouteChange: (next: DocsRoute, options?: { replace?: boolean }) => void;
    api?: DocsAPI | undefined;
    onOpenIssue?: ((uid: string) => void) | undefined;
    onOpenKataShortId?: ((shortId: string, project?: string, daemonId?: string) => void) | undefined;
    // Snapshot of issues currently loaded in the tasks store. Powers
    // the immediate `#` autocomplete results before the daemon search
    // returns. May be empty.
    kataIssues?: readonly IssueSummary[] | undefined;
    // Kata daemon API. When supplied, `#` completion merges in matches
    // from api.search so suggestions reach beyond the loaded view.
    kataAPI?: KataAPI | undefined;
  }

  let {
    route,
    onRouteChange,
    api = createDocsAPI(),
    onOpenIssue,
    onOpenKataShortId,
    kataIssues = [],
    kataAPI,
  }: Props = $props();

  // Always pass a search wrapper so the closure can read the live
  // `kataAPI` prop on each call (capturing it once at script-init time
  // would freeze it to `undefined`). The wrapper short-circuits when
  // no api is available so the completion source falls back to the
  // local snapshot without waiting on daemon search. A folder daemon
  // binding scopes async search and its cache key when it still exists
  // in the live roster; otherwise search follows the active daemon.
  const issueCompletionSource = buildIssueCompletionSource(
    buildDocsIssueCompletionOptions({
      folders: () => folders,
      folderId: () => route.folder,
      daemonRoster: getKataDaemonRoster,
      activeDaemon: getActiveKataDaemon,
      kataIssues: () => kataIssues,
      kataSearch: (filters, opts) => {
        if (!kataAPI) throw new Error("kata api not yet wired");
        return kataAPI.search(filters, opts);
      },
    }),
  );
  // Distinct authors/owners across loaded issues. Recomputed on read so
  // it always tracks the latest kataIssues snapshot.
  const mentionCompletionSource = buildMentionCompletionSource(() =>
    collectMentionNames(kataIssues),
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
  let publishSuccess: string | null = $state(null);

  let lastFolderLoaded: string | null = null;
  let lastDocLoaded: string | null = null;
  // Tracks which folder we've already auto-opened a landing page for, so
  // the effect doesn't fight the user when they intentionally clear the
  // doc query and stay on the bare folder.
  let autoOpenedFor: string | null = null;

  // Token guards stale async responses from clobbering newer state when the
  // user switches folders before the previous request resolves.
  let treeRequestID = 0;
  let gitRequestID = 0;
  let docRequestID = 0;

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
  const OUTLINE_COLLAPSED_KEY = "middleman:docs:outline-collapsed";
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

  let currentRouteKey = $derived(docKey(route.folder, route.doc));
  let editReady = $derived(
    docContent !== null && docContentKey === currentRouteKey && !loadingDoc,
  );

  let folderIndex: FolderIndex = $derived(buildFolderIndex(tree));

  async function loadEditor() {
    if (DocMarkdownEditor || editorLoading) return;
    editorLoading = true;
    editorLoadError = null;
    try {
      DocMarkdownEditor = (await import("./DocMarkdownEditor.svelte")).default;
    } catch (err) {
      editorLoadError = err instanceof Error ? err.message : "Could not load editor.";
    } finally {
      editorLoading = false;
    }
  }

  $effect(() => {
    void loadFolders();
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
      const target = event.target as HTMLElement | null;
      if (target) {
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
    void loadTree(folderID);
    void loadGitStatus(folderID);
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
    void loadDoc(folderID, docPath);
  });

  async function loadFolders() {
    loadingFolders = true;
    try {
      const result = await api.listFolders();
      folders = result;
      foldersError = null;
      if (!route.folder && result.length > 0) {
        // Auto-pick the first folder on landing so a fresh /docs visit isn't
        // a dead end. Preserve `doc` so a shared link like /docs?doc=foo.md
        // still opens the named doc after the folder gets filled in; null it
        // out and the landing-doc effect would race and hijack the URL.
        // Use replaceState so the back button skips the bare /docs URL
        // instead of bouncing back into another auto-select.
        const target = result[0]!.id;
        if (route.doc) {
          // We're honoring an explicit doc query — claim the landing slot
          // for this folder so the landing-doc effect doesn't auto-open
          // README later if the user deletes the named doc.
          autoOpenedFor = target;
        }
        onRouteChange(
          { mode: "docs", folder: target, doc: route.doc },
          { replace: true },
        );
      }
    } catch (err) {
      foldersError = err instanceof Error ? err.message : "Failed to load folders";
    } finally {
      loadingFolders = false;
    }
  }

  async function loadTree(folderID: string) {
    const token = ++treeRequestID;
    loadingTree = true;
    treeError = null;
    try {
      const result = await api.tree(folderID);
      if (token !== treeRequestID) return;
      tree = result;
    } catch (err) {
      if (token !== treeRequestID) return;
      tree = null;
      treeError = err instanceof Error ? err.message : "Failed to load tree";
    } finally {
      if (token === treeRequestID) loadingTree = false;
    }
  }

  async function loadGitStatus(folderID: string) {
    const token = ++gitRequestID;
    try {
      const result = await api.gitStatus(folderID);
      if (token !== gitRequestID) return;
      gitEntriesByFolder = { ...gitEntriesByFolder, [folderID]: result.entries };
      folderIsRepo = { ...folderIsRepo, [folderID]: result.is_repo };
    } catch (err) {
      // Git status is decorative — failure shouldn't break the tree.
      // But unsafe_git_config means the folder IS a repo that the server
      // refused to inspect for safety; keep the publish action visible so
      // the dialog can surface the explanation instead of silently
      // dropping publish.
      if (token !== gitRequestID) return;
      gitEntriesByFolder = { ...gitEntriesByFolder, [folderID]: [] };
      const unsafeRepo = (err as DocsAPIError)?.code === "unsafe_git_config";
      folderIsRepo = { ...folderIsRepo, [folderID]: unsafeRepo };
    }
  }

  async function loadDoc(folderID: string, docPath: string) {
    const token = ++docRequestID;
    loadingDoc = true;
    docError = null;
    try {
      const content = await api.readFile(folderID, docPath);
      if (token !== docRequestID) return;
      docContent = content;
      docContentKey = docKey(folderID, docPath);
    } catch (err) {
      if (token !== docRequestID) return;
      docContent = null;
      docContentKey = null;
      docError = err instanceof Error ? err.message : "Failed to load document";
    } finally {
      if (token === docRequestID) loadingDoc = false;
    }
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

  function handleIssueLink(uid: string) {
    onOpenIssue?.(uid);
  }

  function handleKataShortIdLink(shortId: string, project?: string) {
    onOpenKataShortId?.(shortId, project, folderDaemon());
  }

  function folderDaemon(): string | undefined {
    return effectiveDocsFolderDaemon(folders, route.folder, getKataDaemonRoster());
  }

  async function beginEdit() {
    // Refuse to enter edit mode unless the loaded body belongs to the
    // route currently in view — guards against the race where the user
    // navigates to doc B, then clicks Edit before B has finished
    // loading, and edits A's body into B's path.
    if (!editReady || docContent === null) return;
    if (!route.folder || !route.doc) return;
    await loadEditor();
    if (!DocMarkdownEditor) return;
    editorDraft = docContent;
    editorDirty = false;
    saveError = null;
    editing = true;
    editTarget = { folder: route.folder, doc: route.doc };
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

  async function saveEdit(value: string) {
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
    try {
      await api.writeFile(target.folder, target.doc, value);
      // Only refresh the in-view doc if it's still the one we wrote to.
      const targetKey = docKey(target.folder, target.doc);
      const currentKey = docKey(route.folder, route.doc);
      if (currentKey === targetKey) {
        docContent = value;
        docContentKey = currentKey;
      }
      editing = false;
      editorDirty = false;
      editTarget = null;
    } catch (err) {
      saveError = err instanceof Error ? err.message : "Failed to save";
    } finally {
      saving = false;
    }
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

  function isFolderActive(id: string): boolean {
    return route.folder === id;
  }

  let activeFolder = $derived(folders.find((folder) => folder.id === route.folder));
  let activeFolderName = $derived(activeFolder?.name ?? "Docs");
  let staleFolderDaemon = $derived.by(() => {
    if (!getKataDaemonRosterLoaded()) return undefined;
    const daemon = activeFolder?.daemon?.trim();
    if (!daemon) return undefined;
    return getKataDaemonRoster().includes(daemon) ? undefined : daemon;
  });

  let activeFolderGit = $derived(
    route.folder ? gitEntriesByFolder[route.folder] ?? [] : [],
  );
  let activeFolderIsRepo = $derived(
    route.folder ? folderIsRepo[route.folder] === true : false,
  );

  let folderMenuOpen = $state(false);
  let folderMenuRoot: HTMLDivElement | null = $state(null);

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

  async function submitNewFile() {
    if (!route.folder) return;
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
    try {
      await api.createFile(route.folder, name, "");
      await loadTree(route.folder);
      newFileOpen = false;
      onRouteChange({ mode: "docs", folder: route.folder, doc: name });
    } catch (err) {
      newFileError = describeFileError(err, "Could not create file.");
    } finally {
      newFileSaving = false;
    }
  }

  // Right-click → Rename in the file tree resolves here. The from/to
  // paths come straight from @pierre/trees' inline rename input, so
  // they're already scoped to the active folder and may target any
  // file in the tree (not just route.doc). We rethrow API errors so
  // the library can keep the inline input open and surface the
  // failure to the user.
  async function handleInlineRename(from: string, to: string): Promise<void> {
    if (!route.folder) return;
    if (from === to) return;
    inlineFileError = null;
    try {
      // The dirty-editor guard lives INSIDE the catch path: @pierre/trees
      // already moved the item locally before this handler runs, and the
      // catch below reloads the canonical tree to clear that phantom.
      // Throwing before the try/catch would skip the reload AND skip the
      // inlineFileError surface, leaving the tree showing a path that
      // doesn't exist on disk — the same bug the catch path is meant to
      // prevent.
      if (editing && editorDirty && from === route.doc) {
        throw new Error("Save or cancel the open edit before renaming.");
      }
      await api.renameFile(route.folder, from, to);
      await loadTree(route.folder);
      // If we renamed the file currently in view, follow the move so
      // the URL and breadcrumb don't keep pointing at the old path.
      if (from === route.doc) {
        onRouteChange(
          { mode: "docs", folder: route.folder, doc: to },
          { replace: true },
        );
      }
    } catch (err) {
      // @pierre/trees already moved the item locally and FolderTree's
      // .catch() swallows the promise rejection, so we'd be left with a
      // phantom path in the tree until something else triggers a reload.
      // Reload the canonical tree to clear the phantom, surface the
      // failure to the user via inlineFileError so they understand why
      // the rename "didn't take", and rethrow so the original promise
      // chain still rejects for any other observer.
      const reason = describeFileError(err, "Could not rename file.");
      inlineFileError = reason;
      try {
        await loadTree(route.folder);
      } catch {
        // loadTree's own treeError will be surfaced separately by the
        // tree panel; nothing more to do here.
      }
      throw err;
    }
  }

  async function submitRename() {
    if (!route.folder || !route.doc) return;
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
    const parent = route.doc.includes("/")
      ? route.doc.slice(0, route.doc.lastIndexOf("/") + 1)
      : "";
    const newPath = `${parent}${target}`;
    if (newPath === route.doc) {
      renameOpen = false;
      return;
    }
    renameSaving = true;
    renameError = null;
    try {
      await api.renameFile(route.folder, route.doc, newPath);
      await loadTree(route.folder);
      renameOpen = false;
      onRouteChange(
        { mode: "docs", folder: route.folder, doc: newPath },
        { replace: true },
      );
    } catch (err) {
      renameError = describeFileError(err, "Could not rename file.");
    } finally {
      renameSaving = false;
    }
  }

  async function submitDelete() {
    if (!route.folder || !route.doc) return;
    if (editing && editorDirty) {
      deleteError = "Save or cancel the open edit before deleting.";
      return;
    }
    deleting = true;
    deleteError = null;
    try {
      await api.deleteFile(route.folder, route.doc);
      await loadTree(route.folder);
      deleteOpen = false;
      onRouteChange(
        { mode: "docs", folder: route.folder, doc: null },
        { replace: true },
      );
    } catch (err) {
      deleteError = describeFileError(err, "Could not delete file.");
    } finally {
      deleting = false;
    }
  }

  function describeFileError(err: unknown, fallback: string): string {
    const e = err as DocsAPIError;
    if (e?.code === "already_exists") return "A file with that name already exists.";
    if (e?.code === "unsupported_extension") return "Only .md files are supported.";
    if (e?.code === "outside_folder") return "That path isn't allowed.";
    if (e?.message) return e.message;
    return fallback;
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

  function toggleFolderMenu() {
    folderMenuOpen = !folderMenuOpen;
  }

  function closeFolderMenu() {
    folderMenuOpen = false;
  }

  function pickFolder(id: string) {
    selectFolder(id);
    closeFolderMenu();
  }

  function handleFolderMenuKeydown(event: KeyboardEvent) {
    if (event.key === "Escape") closeFolderMenu();
  }

  function openAddFolder() {
    closeFolderMenu();
    addFolderOpen = true;
  }

  async function handleFolderAdded(folder: Folder) {
    addFolderOpen = false;
    await loadFolders();
    onRouteChange({ mode: "docs", folder: folder.id, doc: null });
  }

  function openRenameFolder(folder: Folder) {
    closeFolderMenu();
    renameFolderTarget = folder;
    renameFolderValue = folder.name;
    renameFolderError = null;
  }

  async function submitRenameFolder() {
    if (!renameFolderTarget) return;
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
    try {
      const updated = await api.renameFolder(target.id, next);
      folders = folders.map((n) => (n.id === target.id ? updated : n));
      renameFolderTarget = null;
    } catch (err) {
      renameFolderError = describeFileError(err, "Could not rename folder.");
    } finally {
      renameFolderSaving = false;
    }
  }

  function openRemoveFolder(folder: Folder) {
    closeFolderMenu();
    removeFolderTarget = folder;
    removeFolderError = null;
  }

  async function submitRemoveFolder() {
    if (!removeFolderTarget) return;
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
    try {
      await api.removeFolder(target.id);
      const remaining = folders.filter((n) => n.id !== target.id);
      folders = remaining;
      removeFolderTarget = null;
      // If we just removed the active folder, fall back to whatever
      // remains so the docs view doesn't dead-end on a stale id.
      if (route.folder === target.id) {
        autoOpenedFor = null;
        const fallback = remaining[0]?.id ?? null;
        onRouteChange({ mode: "docs", folder: fallback, doc: null });
      }
    } catch (err) {
      removeFolderError = describeFileError(err, "Could not remove folder.");
    } finally {
      removingFolder = false;
    }
  }

  $effect(() => {
    if (!folderMenuOpen) return;
    function onPointerDown(event: PointerEvent) {
      if (!folderMenuRoot) return;
      if (event.target instanceof Node && folderMenuRoot.contains(event.target)) return;
      closeFolderMenu();
    }
    window.addEventListener("pointerdown", onPointerDown, true);
    return () => window.removeEventListener("pointerdown", onPointerDown, true);
  });

  async function onPublishedSuccess(result: GitPublishResponse) {
    publishSuccess =
      `Committed and pushed ${result.files.length} ${result.files.length === 1 ? "file" : "files"} as ${result.short_commit}.`;
    if (route.folder) await loadGitStatus(route.folder);
  }
</script>

<div class="docs-workspace" bind:this={workspaceRoot}>
  <div class="docs-list">
    <div
      class="list-header"
      bind:this={folderMenuRoot}
      onkeydown={handleFolderMenuKeydown}
      role="presentation"
    >
      {#if !loadingFolders && folders.length === 0 && !foldersError}
        <!-- No folders configured AND no load error: the chip itself is
             the "Add folder" CTA so the user isn't staring at a
             disabled "Docs" label with a greyed-out + next to it.
             When loading failed we keep the regular menu chip below so
             the error message inside the menu remains reachable. -->
        <button
          type="button"
          class="folder-chip folder-chip--add"
          onclick={openAddFolder}
        >
          <FolderIcon size={14} strokeWidth={1.75} />
          <span class="folder-chip-name">Add folder…</span>
          <Plus size={14} strokeWidth={2} />
        </button>
      {:else}
        <button
          type="button"
          class="folder-chip"
          aria-label="Switch folder"
          aria-haspopup="listbox"
          aria-expanded={folderMenuOpen}
          onclick={toggleFolderMenu}
          disabled={loadingFolders}
        >
          <FolderIcon size={14} strokeWidth={1.75} />
          <span class="folder-chip-name">{activeFolderName}</span>
          <ChevronDown size={14} strokeWidth={1.75} />
        </button>
        {#if route.folder}
          <button
            type="button"
            class="list-action"
            aria-label="New file"
            title="New file"
            onclick={openNewFileModal}
          >
            <Plus size={14} strokeWidth={2} />
          </button>
          {#if activeFolderIsRepo}
            <button
              type="button"
              class="list-action"
              aria-label="Publish to git"
              title="Commit & push to git"
              onclick={() => (publishOpen = true)}
            >
              <Upload size={14} strokeWidth={1.75} />
            </button>
          {/if}
        {/if}
      {/if}
      {#if folderMenuOpen}
        <ul class="folder-menu" role="listbox" aria-label="Folders">
          {#if foldersError}
            <li class="folder-menu-msg error">{foldersError}</li>
          {:else if folders.length === 0}
            <li class="folder-menu-msg muted">No folders configured.</li>
          {:else}
            {#each folders as folder (folder.id)}
              <li class="folder-menu-li">
                <button
                  type="button"
                  class="folder-menu-row"
                  class:active={isFolderActive(folder.id)}
                  role="option"
                  aria-selected={isFolderActive(folder.id)}
                  onclick={() => pickFolder(folder.id)}
                >
                  <span class="folder-menu-check" aria-hidden="true">
                    {#if isFolderActive(folder.id)}
                      <Check size={13} strokeWidth={2} />
                    {/if}
                  </span>
                  <span class="folder-menu-name">{folder.name}</span>
                </button>
                <div class="folder-menu-actions">
                  <button
                    type="button"
                    class="folder-menu-icon"
                    aria-label={`Rename ${folder.name}`}
                    title="Rename"
                    onclick={(event) => {
                      event.stopPropagation();
                      openRenameFolder(folder);
                    }}
                  >
                    <Pencil size={12} strokeWidth={1.75} />
                  </button>
                  <button
                    type="button"
                    class="folder-menu-icon folder-menu-icon--danger"
                    aria-label={`Remove ${folder.name}`}
                    title="Remove"
                    onclick={(event) => {
                      event.stopPropagation();
                      openRemoveFolder(folder);
                    }}
                  >
                    <Trash2 size={12} strokeWidth={1.75} />
                  </button>
                </div>
              </li>
            {/each}
          {/if}
          <li class="folder-menu-divider" role="presentation"></li>
          <li>
            <button
              type="button"
              class="folder-menu-add"
              onclick={openAddFolder}
            >
              <Plus size={13} strokeWidth={2} />
              <span>Add folder…</span>
            </button>
          </li>
        </ul>
      {/if}
    </div>

    <div class="list-body">
      {#if staleFolderDaemon}
        <div class="folder-daemon-warning" role="status">
          Daemon {staleFolderDaemon} is not available. Task links use the active daemon.
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
                onclick={() => void beginEdit()}
                disabled={!editReady || editorLoading}
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
                <ul class="file-menu" role="menu" aria-label="File actions">
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
                completionSources={[
                  issueCompletionSource,
                  mentionCompletionSource,
                  wikilinkCompletionSource,
                ]}
              />
            {:else}
              <div class="editor-load-state" role="status">
                {#if editorLoadError}
                  <span>Editor failed to load.</span>
                  <button type="button" class="toolbar-btn" onclick={() => void loadEditor()}>Retry</button>
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
              onSelectIssue={handleIssueLink}
              onSelectKataShortId={handleKataShortIdLink}
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
  onClose={() => (newFileOpen = false)}
>
  <form
    class="modal-form"
    onsubmit={(event) => {
      event.preventDefault();
      void submitNewFile();
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
  onClose={() => (renameOpen = false)}
>
  <form
    class="modal-form"
    onsubmit={(event) => {
      event.preventDefault();
      void submitRename();
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
      onclick={() => void submitDelete()}
      disabled={deleting}
    >
      {deleting ? "Deleting…" : "Delete"}
    </button>
  </div>
</Modal>

<AddFolderDialog
  open={addFolderOpen}
  {api}
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

{#if publishSuccess}
  <p class="publish-success" role="status">{publishSuccess}</p>
{/if}

<Modal
  open={renameFolderTarget !== null}
  title="Rename folder"
  onClose={() => (renameFolderTarget = null)}
>
  <form
    class="modal-form"
    onsubmit={(event) => {
      event.preventDefault();
      void submitRenameFolder();
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
  onClose={() => (removeFolderTarget = null)}
>
  <p class="modal-body-text">
    Remove <strong>{removeFolderTarget?.name}</strong> from middleman? The
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
      onclick={() => void submitRemoveFolder()}
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

  .list-action {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    border-radius: var(--radius-sm);
    border: 1px solid transparent;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }

  .list-action:hover:not(:disabled) {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
    border-color: var(--border-muted);
  }

  .list-action:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }

  .folder-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-md);
    font-weight: 600;
    text-align: left;
    cursor: pointer;
    border: 1px solid transparent;
  }

  .folder-chip:hover:not(:disabled),
  .folder-chip[aria-expanded="true"] {
    background: var(--bg-surface-hover);
    border-color: var(--border-muted);
  }

  .folder-chip:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .folder-chip--add {
    color: var(--text-muted);
    font-weight: 500;
    font-style: italic;
  }

  .folder-chip--add:hover {
    color: var(--text-primary);
  }

  .folder-chip-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .folder-chip :global(svg) {
    color: var(--text-muted);
  }

  .folder-menu {
    position: absolute;
    top: calc(100% - 2px);
    left: 8px;
    right: 8px;
    z-index: 30;
    list-style: none;
    margin: 0;
    padding: 4px;
    background: var(--bg-elevated);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-lg);
    max-height: 320px;
    overflow: auto;
  }

  .folder-menu-msg {
    padding: 8px 10px;
    font-size: var(--font-size-sm);
    color: var(--text-muted);
  }

  .folder-menu-msg.error {
    color: var(--accent-red);
  }

  .folder-menu-row {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    background: transparent;
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    text-align: left;
    border: none;
    cursor: pointer;
  }

  .folder-menu-row:hover {
    background: var(--bg-surface-hover);
  }

  .folder-menu-row.active {
    color: var(--accent-blue);
  }

  .folder-menu-check {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 14px;
    color: var(--accent-blue);
  }

  .folder-menu-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .folder-menu-li {
    position: relative;
    display: flex;
    align-items: center;
  }

  .folder-menu-li .folder-menu-row {
    flex: 1;
    padding-right: 56px;
  }

  .folder-menu-actions {
    position: absolute;
    right: 4px;
    top: 50%;
    transform: translateY(-50%);
    display: flex;
    gap: 2px;
    opacity: 0;
    pointer-events: none;
  }

  .folder-menu-li:hover .folder-menu-actions,
  .folder-menu-li:focus-within .folder-menu-actions {
    opacity: 1;
    pointer-events: auto;
  }

  .folder-menu-icon {
    width: 22px;
    height: 22px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: none;
    background: none;
    border-radius: var(--radius-sm);
    color: var(--text-muted);
    cursor: pointer;
  }

  .folder-menu-icon:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
  }

  .folder-menu-icon--danger:hover {
    color: var(--accent-red);
  }

  .folder-menu-divider {
    height: 1px;
    background: var(--border-default);
    margin: 4px 0;
    list-style: none;
  }

  .folder-menu-add {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 6px 8px;
    border: none;
    background: none;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
    text-align: left;
    cursor: pointer;
    border-radius: var(--radius-sm);
  }

  .folder-menu-add:hover {
    background: var(--bg-surface-hover);
    color: var(--text-primary);
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
    background: rgba(193, 74, 60, 0.08);
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
    background: var(--bg-elevated);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-lg);
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
    gap: 10px;
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
    background: rgba(193, 74, 60, 0.1);
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
    border-radius: var(--radius-md);
    background: var(--bg-elevated);
    border: 1px solid var(--border-default);
    color: var(--text-primary);
    font-size: var(--font-size-sm);
    box-shadow: var(--shadow-lg);
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
    gap: 10px;
    color: var(--text-muted);
    font-size: var(--font-size-sm);
  }

  @media (max-width: 1100px) {
    .doc-pane {
      grid-template-columns: minmax(0, 1fr);
    }
    .doc-pane :global(.doc-outline) {
      display: none;
    }
  }
</style>
