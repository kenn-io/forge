import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { IssueDetail } from "../../api/types.js";
import { ACTIONS_KEY, API_CLIENT_KEY, NAVIGATE_KEY, STORES_KEY, UI_CONFIG_KEY } from "../../context.js";
import { createDetailActivityViewStore } from "../../stores/detail-activity-view.svelte.js";
import { dismissFlash, getFlashes } from "../../stores/flash.svelte.js";
import { resetWorkspaceCreatePendingForTest } from "../../stores/workspace-create-pending.svelte.js";
import type { InlineWorkspaceController, WorkspaceItemIdentity } from "../../workspace-inline.js";
import { openLabelPickerFor } from "./labelPickerCommand.js";
import { createTestController } from "../workspace/WorkspaceDockPanelTestController.svelte.js";

// The pending-create store is module-scoped so it can survive component
// remounts; tests that leave a deferred create unresolved must not leak
// that pending identity into later tests.
afterEach(resetWorkspaceCreatePendingForTest);

const clipboardMockState = vi.hoisted(() => ({
  resolvers: [] as Array<(ok: boolean) => void>,
}));

vi.mock("@kenn-io/kit-ui", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@kenn-io/kit-ui")>();
  return {
    ...actual,
    copyToClipboard: vi.fn(() => new Promise<boolean>((resolve) => clipboardMockState.resolvers.push(resolve))),
  };
});

import IssueDetailComponent from "./IssueDetail.svelte";

const capabilities = {
  read_repositories: true,
  read_merge_requests: true,
  read_issues: true,
  read_comments: true,
  read_releases: true,
  read_ci: true,
  read_labels: false,
  comment_mutation: false,
  state_mutation: true,
  merge_mutation: false,
  review_mutation: false,
  workflow_approval: false,
  ready_for_review: false,
  draft_mutation: false,
  issue_mutation: true,
  label_mutation: false,
  assignee_mutation: false,
  reviewer_mutation: false,
  thread_reply: false,
  thread_resolve: false,
  review_draft_mutation: false,
  review_thread_resolution: false,
  review_suggestion_application: false,
  read_review_threads: false,
  native_multiline_ranges: false,
  mutation_head_binding: false,
  supported_review_actions: [],
};

function issueDetail(): IssueDetail {
  return {
    detail_loaded: true,
    detail_fetched_at: "2026-05-01T12:05:00Z",
    platform_host: "github.com",
    repo_owner: "acme",
    repo_name: "widget",
    workspace: undefined,
    repo: {
      ID: 1,
      Owner: "acme",
      Name: "widget",
      Host: "github.com",
      PlatformHost: "github.com",
      Platform: "github",
      URL: "https://github.com/acme/widget",
      DefaultBranch: "main",
      IsArchived: false,
      AllowSquashMerge: false,
      AllowMergeCommit: false,
      AllowRebaseMerge: false,
      capabilities,
      provider: "github",
      platform_host: "github.com",
      owner: "acme",
      name: "widget",
      repo_path: "acme/widget",
    },
    issue: {
      ID: 1,
      RepoID: 1,
      PlatformID: 100,
      PlatformExternalID: "ISSUE_1",
      Number: 7,
      URL: "https://github.com/acme/widget/issues/7",
      Title: "Track compact issue activity",
      Author: "octocat",
      Body: "",
      State: "open",
      CommentCount: 1,
      CreatedAt: "2026-05-01T11:00:00Z",
      UpdatedAt: "2026-05-01T12:00:00Z",
      LastActivityAt: "2026-05-01T12:00:00Z",
      ClosedAt: null,
      DetailFetchedAt: "2026-05-01T12:05:00Z",
      Starred: false,
      labels: [],
      assignees: [],
      detail_loaded: true,
      repo: {
        provider: "github",
        platform_host: "github.com",
        owner: "acme",
        name: "widget",
        repo_path: "acme/widget",
      },
      repo_owner: "acme",
      repo_name: "widget",
      platform_host: "github.com",
    },
    events: [
      {
        ID: 20,
        IssueID: 1,
        PlatformID: 20,
        PlatformExternalID: "",
        EventType: "issue_comment",
        Author: "alice",
        Summary: "",
        Body: "Issue **activity** preview",
        MetadataJSON: "",
        CreatedAt: "2026-05-01T12:01:00Z",
        DedupeKey: "issue-comment-20",
        DirectURL: "",
        ThreadID: null,
      },
    ],
  };
}

function renderIssueDetail(
  detail: IssueDetail,
  deleteIssueComment = vi.fn(async () => true),
  options: { staleRefreshing?: boolean; inlineWorkspace?: InlineWorkspaceController | null } = {},
  apiClient: { GET: ReturnType<typeof vi.fn>; POST: ReturnType<typeof vi.fn> } = {
    GET: vi.fn(),
    POST: vi.fn(),
  },
) {
  const issuesStore = {
    loadIssueDetail: vi.fn(async () => undefined),
    startIssueDetailPolling: vi.fn(),
    stopIssueDetailPolling: vi.fn(),
    getIssueDetail: () => detail,
    isIssueDetailLoading: () => false,
    getIssueDetailError: () => null,
    isIssueStaleRefreshing: () => options.staleRefreshing ?? false,
    isIssueDetailSyncing: () => false,
    getIssueDetailLoaded: () => true,
    loadIssues: vi.fn(async () => undefined),
    updateIssueKanbanState: vi.fn(),
    toggleIssueStar: vi.fn(),
    editIssueComment: vi.fn(),
    deleteIssueComment,
    setIssueLabels: vi.fn(),
    setIssueAssignees: vi.fn(),
    saveIssueBodyInBackground: vi.fn(),
    setLocalIssueBody: vi.fn(),
  };
  const navigate = vi.fn();

  const result = render(IssueDetailComponent, {
    props: {
      owner: "acme",
      name: "widget",
      number: detail.issue.Number,
      provider: "github",
      platformHost: "github.com",
      repoPath: "acme/widget",
      inlineWorkspace: options.inlineWorkspace ?? null,
    },
    context: new Map<symbol, unknown>([
      [API_CLIENT_KEY, apiClient],
      [
        STORES_KEY,
        {
          issues: issuesStore,
          activity: { loadActivity: vi.fn() },
          detailActivityView: createDetailActivityViewStore(),
        },
      ],
      [ACTIONS_KEY, { issue: [] }],
      [UI_CONFIG_KEY, { hideStar: true }],
      [NAVIGATE_KEY, navigate],
    ]),
  });
  return { ...result, deleteIssueComment, issuesStore, navigate };
}

describe("IssueDetail activity view", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("offers compact activity rows from the shared View menu without PR filters", async () => {
    const { container } = renderIssueDetail(issueDetail());

    await fireEvent.click(screen.getByRole("button", { name: /view/i }));

    expect(screen.getByRole("button", { name: /normal/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /compact/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /messages/i })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: /compact/i }));

    expect(localStorage.getItem("middleman-detail-activity-view")).toBe("compact");
    expect(container.querySelectorAll(".event-card--compact-row")).toHaveLength(1);
    expect(container.textContent).toContain("Issue activity preview");
  });

  it("explains that creating a workspace enables agent sessions", () => {
    renderIssueDetail(issueDetail());

    const button = screen.getByRole("button", { name: "Create Workspace" });
    expect(button.getAttribute("title")).toContain("issue worktree");
    expect(button.getAttribute("title")).toContain("launch agents");
    expect(button.getAttribute("title")).toContain("shells");
    const descriptionId = button.getAttribute("aria-describedby");
    expect(descriptionId).toBeTruthy();
    expect(document.getElementById(descriptionId ?? "")?.textContent).toContain(button.getAttribute("title"));
  });

  it("labels an active stale-detail refresh", () => {
    renderIssueDetail(issueDetail(), undefined, { staleRefreshing: true });

    expect(screen.getByLabelText("Refreshing issue details")).toBeTruthy();
    expect(screen.getByText("Refreshing...")).toBeTruthy();
  });

  it("deletes an ordinary issue comment through the issues store", async () => {
    const detail = issueDetail();
    detail.repo.capabilities.comment_mutation = true;
    detail.repo.operations = {
      delete_comment: { available: true },
    } as IssueDetail["repo"]["operations"];
    const { deleteIssueComment } = renderIssueDetail(detail);

    await fireEvent.click(screen.getByRole("button", { name: "Delete comment" }));
    await fireEvent.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => {
      expect(deleteIssueComment).toHaveBeenCalledWith("acme", "widget", 7, 20);
      expect(screen.queryByRole("dialog", { name: "Delete comment?" })).toBeNull();
    });
  });

  it("hides issue comment deletion when the operation is unavailable", () => {
    const detail = issueDetail();
    detail.repo.capabilities.comment_mutation = true;
    detail.repo.operations = {
      delete_comment: {
        available: false,
        code: "missing_write_credential",
        unavailable_reason: "No user credential for writes on github.com",
      },
    } as IssueDetail["repo"]["operations"];

    renderIssueDetail(detail);

    expect(screen.queryByRole("button", { name: "Delete comment" })).toBeNull();
  });
});

describe("IssueDetail inline workspace handoff", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    for (const item of getFlashes()) dismissFlash(item.id);
  });

  const identity: WorkspaceItemIdentity = {
    provider: "github",
    platformHost: "github.com",
    owner: "acme",
    name: "widget",
    repoPath: "acme/widget",
    number: 7,
    itemType: "issue",
  };

  function deferredWorkspaceApiClient() {
    let resolvePost!: (value: { data?: { id: string; status: string } }) => void;
    const postPromise = new Promise<{ data?: { id: string; status: string } }>((resolve) => {
      resolvePost = resolve;
    });
    const apiClient = {
      GET: vi.fn(),
      POST: vi.fn(async () => postPromise),
    };
    return { apiClient, resolvePost };
  }

  it("create with inline controller records the override and does not navigate", async () => {
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { issuesStore, navigate } = renderIssueDetail(
      issueDetail(),
      undefined,
      { inlineWorkspace: controller },
      apiClient,
    );

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });

    await vi.waitFor(() => {
      expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    });
    expect(navigate).not.toHaveBeenCalled();
    await vi.waitFor(() => {
      expect(issuesStore.loadIssueDetail).toHaveBeenCalledWith("acme", "widget", 7, {
        provider: "github",
        platformHost: "github.com",
        repoPath: "acme/widget",
      });
    });
  });

  it("records the override when a layout change unmounts the detail mid-create", async () => {
    // Drawer tab and layout changes unmount IssueDetail with the same
    // issue still selected; the successful response must still land its
    // store-level override instead of orphaning the created workspace.
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { issuesStore, navigate, unmount } = renderIssueDetail(
      issueDetail(),
      undefined,
      { inlineWorkspace: controller },
      apiClient,
    );

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    const loadCallsBeforeUnmount = issuesStore.loadIssueDetail.mock.calls.length;
    unmount();
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });

    await vi.waitFor(() => {
      expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    });
    expect(navigate).not.toHaveBeenCalled();
    // No refetch from a destroyed component: its frozen identity cannot
    // see that the shared issue store may belong to a new selection.
    expect(issuesStore.loadIssueDetail.mock.calls.length).toBe(loadCallsBeforeUnmount);
  });

  it("an alias-only route re-expression does not discard an in-flight create", async () => {
    // gh vs github and omitted vs concrete default host describe the same
    // issue; such a prop change mid-create must not bump the request
    // generation, or the success would be discarded and the button
    // re-enabled for a duplicate request.
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { rerender } = renderIssueDetail(issueDetail(), undefined, { inlineWorkspace: controller }, apiClient);

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    await rerender({ provider: "gh", platformHost: undefined });
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });

    await vi.waitFor(() => {
      expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    });
  });

  it("keeps mutation actions live when the identity omits the host or aliases the provider", async () => {
    // Activity URLs may omit platform_host and route segments may carry
    // gh/gl aliases while the payload is canonical and concrete; the
    // stale guard must not block mutations on a detail that is current.
    const controller = createTestController("split");
    const { apiClient } = deferredWorkspaceApiClient();
    const { rerender } = renderIssueDetail(issueDetail(), undefined, { inlineWorkspace: controller }, apiClient);

    await rerender({ provider: "gh", platformHost: undefined });
    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));

    expect(apiClient.POST).toHaveBeenCalled();
  });

  it("publishes a confirmed creation even after the selection changed", async () => {
    // The workspace exists server-side the moment the response confirms
    // it. Discarding it because the selection moved on would leave the
    // next visit to this issue offering "Create Workspace" again — a
    // duplicate submission. Only presentation (refetch, navigation)
    // stays tied to the live selection.
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { issuesStore, navigate, rerender } = renderIssueDetail(
      issueDetail(),
      undefined,
      { inlineWorkspace: controller },
      apiClient,
    );

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    // Navigate to a different issue while the create request is in flight.
    // The route change legitimately triggers its own loadIssueDetail call,
    // so the assertion below checks the call count doesn't grow further
    // once the stale create response lands, not that it's zero.
    await rerender({ number: 8 });
    const loadCallsAfterRerender = issuesStore.loadIssueDetail.mock.calls.length;
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    expect(issuesStore.loadIssueDetail.mock.calls.length).toBe(loadCallsAfterRerender);
    expect(navigate).not.toHaveBeenCalled();
  });

  it("keeps Create Workspace disabled across a selection round-trip while a create is pending", async () => {
    // The local creating flag is cleared by the route-reset effect on
    // A→B and again on B→A; only the shared identity-keyed pending
    // store can keep the button disabled, or the second click sends a
    // duplicate create and earns a misleading "already exists" conflict.
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { rerender } = renderIssueDetail(issueDetail(), undefined, { inlineWorkspace: controller }, apiClient);

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    await rerender({ number: 8 });
    await rerender({ number: 7 });

    const button = screen.getByRole("button", { name: "Creating..." });
    expect(button.hasAttribute("disabled")).toBe(true);
    await fireEvent.click(button);
    expect(apiClient.POST).toHaveBeenCalledTimes(1);

    resolvePost({ data: { id: "ws-new", status: "provisioning" } });
    await vi.waitFor(() => {
      expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    });
    expect(controller.recordCreated).toHaveBeenCalledTimes(1);
  });

  it("publishes a confirmed creation across a selection round-trip", async () => {
    // A→B→A: returning to the original issue restores an identity that
    // matches the request, but the round-trip bumped the request
    // generation. The confirmed creation must still land its override,
    // or the re-rendered detail shows "Create Workspace" until an
    // unrelated refetch.
    const controller = createTestController("split");
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { rerender } = renderIssueDetail(issueDetail(), undefined, { inlineWorkspace: controller }, apiClient);

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    await rerender({ number: 8 });
    await rerender({ number: 7 });
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });

    await vi.waitFor(() => {
      expect(controller.recordCreated).toHaveBeenCalledWith(identity, { id: "ws-new", status: "provisioning" });
    });
  });

  it("discards a create failure that lands after the selection changed (no flash)", async () => {
    const controller = createTestController("split");
    let resolvePost!: (value: { error?: { detail?: string } }) => void;
    const postPromise = new Promise<{ error?: { detail?: string } }>((resolve) => {
      resolvePost = resolve;
    });
    const apiClient = {
      GET: vi.fn(),
      POST: vi.fn(async () => postPromise),
    };
    const { rerender } = renderIssueDetail(issueDetail(), undefined, { inlineWorkspace: controller }, apiClient);

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    // Navigate to a different issue while the create request is in flight.
    await rerender({ number: 8 });
    resolvePost({ error: { detail: "boom" } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    // A failure toast about an issue the user already left is noise:
    // without the identity guard this would show "boom".
    expect(getFlashes()).toHaveLength(0);
  });

  it("without a controller create navigates to the terminal (today's behavior)", async () => {
    const apiClient = {
      GET: vi.fn(),
      POST: vi.fn(async () => ({ data: { id: "ws-new", status: "provisioning" } })),
    };
    const { navigate } = renderIssueDetail(issueDetail(), undefined, {}, apiClient);

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));

    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith("/terminal/ws-new"));
  });

  it("discards a create failure that lands after the component unmounted (no flash)", async () => {
    const controller = createTestController("split");
    let resolvePost!: (value: { error?: { detail?: string } }) => void;
    const postPromise = new Promise<{ error?: { detail?: string } }>((resolve) => {
      resolvePost = resolve;
    });
    const apiClient = {
      GET: vi.fn(),
      POST: vi.fn(async () => postPromise),
    };
    renderIssueDetail(issueDetail(), undefined, { inlineWorkspace: controller }, apiClient);

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    cleanup();
    resolvePost({ error: { detail: "boom" } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(getFlashes()).toHaveLength(0);
  });

  it("a late create response cannot navigate after unmount (no controller)", async () => {
    const { apiClient, resolvePost } = deferredWorkspaceApiClient();
    const { navigate } = renderIssueDetail(issueDetail(), undefined, {}, apiClient);

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    cleanup();
    resolvePost({ data: { id: "ws-new", status: "provisioning" } });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(navigate).not.toHaveBeenCalled();
  });

  it("discards a branch-conflict response that lands after the selection changed", async () => {
    const conflictError = {
      type: "urn:middleman:error:issue-workspace-branch-conflict",
      title: "Issue workspace branch conflict",
      detail: "A local branch with the requested name already exists.",
      errors: [
        { message: "Requested branch already exists", location: "body.git_head_ref", value: "middleman/issue-7" },
        {
          message: "Suggested alternative branch name",
          location: "body.suggested_git_head_ref",
          value: "middleman/issue-7-2",
        },
      ],
    };
    let resolvePost!: (value: { error?: typeof conflictError }) => void;
    const postPromise = new Promise<{ error?: typeof conflictError }>((resolve) => {
      resolvePost = resolve;
    });
    const apiClient = {
      GET: vi.fn(),
      POST: vi.fn(async () => postPromise),
    };
    const controller = createTestController("split");
    const { rerender } = renderIssueDetail(issueDetail(), undefined, { inlineWorkspace: controller }, apiClient);

    await fireEvent.click(screen.getByRole("button", { name: "Create Workspace" }));
    // Move to a different issue while the request is in flight, then let
    // the conflict land: the dialog must not pop up over the new issue.
    await rerender({ number: 8 });
    resolvePost({ error: conflictError });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(screen.queryByText("Branch Name Conflict")).toBeNull();
  });

  it("open action becomes focus-terminal with a secondary open-in-workspaces", async () => {
    const detail = issueDetail();
    detail.workspace = { id: "ws-1", status: "ready" };
    const controller = createTestController("split");
    // No override recorded: the controller passes the envelope ref through.
    controller.effectiveWorkspaceRef = vi.fn((_identity, envelopeRef) => envelopeRef ?? null);

    const { navigate } = renderIssueDetail(detail, undefined, { inlineWorkspace: controller });

    await fireEvent.click(screen.getByRole("button", { name: "Focus Terminal" }));
    expect(controller.focusTerminal).toHaveBeenCalled();

    await fireEvent.click(screen.getByRole("button", { name: "Open in Workspaces" }));
    expect(controller.openInWorkspaces).toHaveBeenCalledWith({ id: "ws-1", status: "ready" });
    expect(navigate).not.toHaveBeenCalled();
  });

  it("without a controller open renders a single Open Workspace button that navigates", async () => {
    const detail = issueDetail();
    detail.workspace = { id: "ws-1", status: "ready" };

    const { navigate } = renderIssueDetail(detail);

    expect(screen.queryByRole("button", { name: "Focus Terminal" })).toBeNull();
    await fireEvent.click(screen.getByRole("button", { name: "Open Workspace" }));
    expect(navigate).toHaveBeenCalledWith("/terminal/ws-1");
  });

  it("consults the override layer for button state", () => {
    const detail = issueDetail();
    detail.workspace = undefined;
    const controller = createTestController("split");
    controller.effectiveWorkspaceRef = vi.fn(() => ({ id: "ws-o", status: "ready" }));

    renderIssueDetail(detail, undefined, { inlineWorkspace: controller });

    expect(screen.queryByRole("button", { name: "Create Workspace" })).toBeNull();
    expect(screen.getByRole("button", { name: "Focus Terminal" })).toBeTruthy();
  });

  it("consults the override layer for button state (tombstone hides an envelope workspace)", () => {
    const detail = issueDetail();
    detail.workspace = { id: "ws-1", status: "ready" };
    const controller = createTestController("split");
    controller.effectiveWorkspaceRef = vi.fn(() => null);

    renderIssueDetail(detail, undefined, { inlineWorkspace: controller });

    expect(screen.queryByRole("button", { name: "Focus Terminal" })).toBeNull();
    expect(screen.getByRole("button", { name: "Create Workspace" })).toBeTruthy();
  });

  it("reconciles the override on identity-matched detail load", () => {
    const detail = issueDetail();
    detail.workspace = { id: "ws-1", status: "ready" };
    const controller = createTestController("split");

    renderIssueDetail(detail, undefined, { inlineWorkspace: controller });

    expect(controller.reconcile).toHaveBeenCalledWith(identity, { id: "ws-1", status: "ready" });
  });

  it("reconciles when the identity omits the host and the detail carries the provider default", async () => {
    // Activity URLs may omit platform_host; the loaded detail always
    // carries the concrete default host. The identity-match guard must
    // treat them as one item or the override never reconciles.
    const detail = issueDetail();
    detail.workspace = { id: "ws-1", status: "ready" };
    const controller = createTestController("split");

    const { rerender } = renderIssueDetail(detail, undefined, { inlineWorkspace: controller });
    controller.reconcile.mockClear();

    await rerender({ platformHost: undefined });

    expect(controller.reconcile).toHaveBeenCalledWith(
      { ...identity, platformHost: undefined },
      { id: "ws-1", status: "ready" },
    );
  });

  it("does not reconcile the override for a detail belonging to a different identity", async () => {
    const detail = issueDetail();
    detail.workspace = { id: "ws-1", status: "ready" };
    const controller = createTestController("split");

    const { rerender } = renderIssueDetail(detail, undefined, { inlineWorkspace: controller });
    controller.reconcile.mockClear();

    // Same detail object (still describes issue #7), but props move to a
    // different number: detailMatchesIdentity must now return false so a
    // load for the stale identity can't reconcile an override recorded for
    // the newly-selected issue. Without the guard this would call
    // reconcile with the mismatched identity.
    await rerender({ number: 999 });

    expect(controller.reconcile).not.toHaveBeenCalled();
  });

  it("restores split before opening the label picker while the dock is expanded", async () => {
    // Expanded dock hides this detail (mounted, hidden+inert) but its
    // window-level command listener stays live; without the split reset the
    // picker would open invisibly and pop up on the next collapse.
    const controller = { ...createTestController("expanded"), isClaimedFor: () => true };
    const detail = issueDetail();
    detail.repo.capabilities = { ...capabilities, read_labels: true, label_mutation: true };
    renderIssueDetail(
      detail,
      undefined,
      { inlineWorkspace: controller },
      {
        GET: vi.fn(async () => ({ data: { labels: [] } })),
        POST: vi.fn(),
      },
    );

    openLabelPickerFor({
      itemType: "issue",
      provider: "github",
      platformHost: "github.com",
      owner: "acme",
      name: "widget",
      repoPath: "acme/widget",
      number: 7,
    });

    expect(await screen.findByRole("dialog", { name: "Edit labels" })).toBeTruthy();
    expect(controller.setDockMode).toHaveBeenCalledWith("split");
  });
});

describe("IssueDetail body copy feedback", () => {
  beforeEach(() => {
    localStorage.clear();
    clipboardMockState.resolvers.length = 0;
  });

  afterEach(() => {
    cleanup();
  });

  function copyButton(): HTMLButtonElement {
    const button = document.querySelector<HTMLButtonElement>(".kit-copy-btn.body-copy");
    if (button === null) {
      throw new Error("body copy button not found");
    }
    return button;
  }

  it("shows copied feedback when the clipboard write resolves on the same issue", async () => {
    const detail = issueDetail();
    detail.issue.Body = "body text";
    renderIssueDetail(detail);

    await fireEvent.click(copyButton());
    expect(clipboardMockState.resolvers).toHaveLength(1);
    clipboardMockState.resolvers[0]!(true);
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(document.querySelector(".body-copy--copied")).not.toBeNull();
  });

  it("drops a clipboard write that resolves after navigating to another issue", async () => {
    const detail = issueDetail();
    detail.issue.Body = "body text";
    const { rerender } = renderIssueDetail(detail);

    await fireEvent.click(copyButton());
    expect(clipboardMockState.resolvers).toHaveLength(1);

    // Navigate to a different issue while the clipboard promise is pending.
    await rerender({ number: detail.issue.Number + 1 });

    clipboardMockState.resolvers[0]!(true);
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(document.querySelector(".body-copy--copied")).toBeNull();
  });
});
