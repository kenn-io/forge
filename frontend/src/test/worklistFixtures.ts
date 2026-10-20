import type {
  SetWorklistItemsHandledBody,
  WorklistItemResponse,
  WorklistResponse,
  WorklistSummaryResponse,
} from "../lib/api/generated/models/index.js";
import {
  jsonResponse,
  mockIssueFixture,
  mockPullFixture,
  type MockRequest,
  type MockRouteOverride,
} from "./mockApiFetch.js";

const capabilities = { state_mutation: true, read_labels: true, label_mutation: true, assignee_mutation: true };

const repoIds: Record<string, number> = { "github.com": 1001, "example.com": 2002 };

function repo(owner: string, name: string, platformHost: string) {
  return {
    provider: "github",
    platform_host: platformHost,
    platform_repo_id: repoIds[platformHost],
    owner,
    name,
    repo_path: `${owner}/${name}`,
    capabilities,
  } as unknown as WorklistItemResponse["repo"];
}

const steadyState = {
  state: "open",
  head_sha: "a".repeat(40),
  comment_count: 1,
  ci_status: "success",
  review_decision: "",
  last_activity_at: "2026-10-19T09:00:00Z",
};

type ItemInput = Partial<WorklistItemResponse> & Pick<WorklistItemResponse, "position" | "number" | "item_type">;

export function worklistItem(input: ItemInput): WorklistItemResponse {
  const isPull = input.item_type === "pr";
  return {
    repo: repo("acme", "widgets", "github.com"),
    title: `Item ${input.number}`,
    url: `https://github.com/acme/widgets/${isPull ? "pull" : "issues"}/${input.number}`,
    score: null,
    section: "",
    note: "",
    caveat: "",
    written_at: "2026-10-19T10:00:00Z",
    snapshot: steadyState,
    live: steadyState,
    freshness: "fresh",
    changes: { new_commits: false, new_comments: 0 },
    pull: null,
    issue: null,
    handled_at: null,
    ...input,
  };
}

export function reviewQueueItems(): WorklistItemResponse[] {
  return [
    worklistItem({
      position: 0,
      item_type: "pr",
      number: 42,
      title: "Add browser regression coverage",
      score: 9,
      section: "Ready to merge",
      note: "Approved and green; only a docs nit left.",
      freshness: "changed",
      changes: { new_commits: false, new_comments: 3, ci_from: "success", ci_to: "failure" },
      live: { ...steadyState, comment_count: 4, ci_status: "failure" },
      pull: { ...mockPullFixture(42), MergeableState: "clean" } as unknown as WorklistItemResponse["pull"],
    }),
    worklistItem({
      position: 1,
      item_type: "issue",
      number: 7,
      title: "Theme toggle does not stick",
      score: 5,
      section: "Needs a decision",
      note: "Reporter confirmed on two browsers.",
      caveat: "Could be a duplicate of an older report.",
      issue: mockIssueFixture(7) as unknown as WorklistItemResponse["issue"],
    }),
    worklistItem({
      position: 2,
      item_type: "pr",
      number: 84,
      repo: repo("acme", "widgets", "example.com"),
      title: "Mirror host stub PR",
      score: 7,
      section: "Ready to merge",
      note: "Reviewed last week.",
      freshness: "changed",
      changes: { new_commits: true, new_comments: 0 },
      live: { ...steadyState, head_sha: "b".repeat(40) },
      pull: { ...mockPullFixture(84), MergeableState: "dirty" } as unknown as WorklistItemResponse["pull"],
    }),
    worklistItem({
      position: 3,
      item_type: "pr",
      number: 55,
      title: "Retire the legacy exporter",
      score: 3,
      section: "Needs a decision",
      freshness: "gone",
      live: null,
    }),
  ];
}

export function worklistResponse(
  id: number,
  title: string,
  items: WorklistItemResponse[] = reviewQueueItems(),
): WorklistResponse {
  return {
    id,
    title,
    recipe: "Open pull requests and issues in acme/widgets with activity this week.",
    created_by: "triage-agent",
    workspace_id: "",
    created_at: "2026-10-19T10:00:00Z",
    updated_at: "2026-10-19T10:00:00Z",
    dismissed_at: null,
    items,
  };
}

export function worklistSummary(list: WorklistResponse): WorklistSummaryResponse {
  return {
    id: list.id,
    title: list.title,
    recipe: list.recipe,
    created_by: list.created_by,
    workspace_id: list.workspace_id,
    item_count: list.items.length,
    changed_count: list.items.filter((item) => item.freshness === "changed").length,
    gone_count: list.items.filter((item) => item.freshness === "gone").length,
    handled_count: list.items.filter((item) => item.handled_at !== null).length,
    created_at: list.created_at,
    updated_at: list.updated_at,
    dismissed_at: list.dismissed_at,
  };
}

export interface WorklistServer {
  lists: WorklistResponse[];
  requests: string[];
  /** Bodies of handled-mark requests, in order. */
  marks?: SetWorklistItemsHandledBody[];
}

export const WORKLIST_HANDLED_AT = "2026-10-20T09:30:00Z";

function markItems(server: WorklistServer, list: WorklistResponse, bodyText: string): Response {
  const body = JSON.parse(bodyText) as SetWorklistItemsHandledBody;
  server.marks?.push(body);
  const matches = body.items.map((ref) =>
    list.items.find(
      (item) =>
        item.item_type === ref.item_type &&
        item.number === ref.number &&
        item.repo.provider === ref.provider &&
        item.repo.platform_host === ref.platform_host &&
        item.repo.platform_repo_id === ref.platform_repo_id,
    ),
  );
  const missing = matches.findIndex((item) => item === undefined);
  if (missing !== -1) {
    return jsonResponse(
      {
        title: "Not Found",
        status: 404,
        detail: "item is not on the worklist",
        code: "notFound",
        details: { worklist_id: list.id, item_index: missing },
      },
      404,
    );
  }
  const marked = matches.filter((item) => item !== undefined);
  for (const item of marked) item.handled_at = body.handled ? (item.handled_at ?? WORKLIST_HANDLED_AT) : null;
  return jsonResponse({
    worklist: worklistSummary(list),
    items: marked.map((item) => ({
      repo: item.repo,
      item_type: item.item_type,
      number: item.number,
      handled_at: item.handled_at,
    })),
  });
}

export function worklistRoutes(server: WorklistServer): MockRouteOverride {
  return (request: MockRequest) => {
    const path = request.url.pathname;
    if (!path.startsWith("/api/v1/worklists")) return null;
    server.requests.push(`${request.method} ${path}`);
    if (request.method === "GET" && path === "/api/v1/worklists") {
      return jsonResponse({
        worklists: server.lists.filter((list) => list.dismissed_at === null).map(worklistSummary),
      });
    }
    const match = path.match(/^\/api\/v1\/worklists\/(\d+)(?:\/(dismiss|undismiss|handled))?$/);
    const list = match ? server.lists.find((candidate) => candidate.id === Number(match[1])) : undefined;
    if (!match || !list) {
      return jsonResponse(
        { title: "Not Found", status: 404, detail: "worklist not found", code: "worklistNotFound" },
        404,
      );
    }
    if (request.method === "POST" && match[2] === "handled") return markItems(server, list, request.bodyText);
    if (request.method === "POST" && match[2]) {
      list.dismissed_at = match[2] === "dismiss" ? "2026-10-20T09:00:00Z" : null;
      return jsonResponse(worklistSummary(list));
    }
    if (request.method === "GET" && !match[2]) return jsonResponse(list);
    return null;
  };
}
