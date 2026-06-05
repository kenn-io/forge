import { describe, expect, it } from "vite-plus/test";
import type { PullRequest } from "../../api/types.js";
import { kanbanDragPayloadFromPull, parseKanbanDragPayload, providerRouteRefFromKanbanDragPayload } from "./drag.js";

describe("kanban drag payloads", () => {
  it("preserves provider repository identity", () => {
    const pr = {
      Number: 17,
      repo: {
        provider: "gitlab",
        platform_host: "gitlab.example.com",
        owner: "group/subgroup",
        name: "project",
        repo_path: "group/subgroup/project",
      },
    } as PullRequest;

    expect(kanbanDragPayloadFromPull(pr)).toEqual({
      provider: "gitlab",
      platformHost: "gitlab.example.com",
      owner: "group/subgroup",
      name: "project",
      repoPath: "group/subgroup/project",
      number: 17,
    });
  });

  it("builds provider route refs without defaulting missing identity", () => {
    expect(() =>
      providerRouteRefFromKanbanDragPayload({
        provider: "",
        platformHost: "gitlab.example.com",
        owner: "group/subgroup",
        name: "project",
        repoPath: "group/subgroup/project",
        number: 17,
      }),
    ).toThrow("provider");
  });

  it("rejects drag payloads without item numbers", () => {
    expect(() =>
      parseKanbanDragPayload(
        JSON.stringify({
          provider: "gitlab",
          platformHost: "gitlab.example.com",
          owner: "group/subgroup",
          name: "project",
          repoPath: "group/subgroup/project",
        }),
      ),
    ).toThrow("number");
  });
});
