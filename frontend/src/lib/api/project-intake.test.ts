import { layer } from "@effect/vitest";
import { Effect } from "effect";
import { beforeEach, describe, expect, vi } from "vite-plus/test";

import { GeneratedApiLive } from "./generated-api.js";
import {
  cloneProject,
  listUserRepositories,
  projectIntakeFailureMessage,
  registerExistingProject,
} from "./project-intake.ts";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}));

vi.mock("./runtime.ts", () => ({
  createRuntimeClient: () => ({
    GET: mocks.get,
    POST: mocks.post,
  }),
}));

function project(id: string) {
  return {
    created_at: "2026-08-04T00:00:00Z",
    display_name: "repo",
    id,
    local_path: "/repo",
    updated_at: "2026-08-04T00:00:00Z",
  };
}

describe("project-intake api", () => {
  beforeEach(() => {
    mocks.get.mockReset();
    mocks.post.mockReset();
  });

  layer(GeneratedApiLive)((it) => {
    it.effect("validates an existing path before registering the root", () =>
      Effect.gen(function* () {
        mocks.get.mockResolvedValue({
          data: { is_valid: true, root_path: "/repo" },
          response: new Response(),
        });
        mocks.post.mockResolvedValue({
          data: project("prj_1"),
          response: new Response(),
        });

        const result = yield* registerExistingProject("/repo/subdir");

        expect(result).toMatchObject({ id: "prj_1" });
        expect(mocks.get).toHaveBeenCalledWith("/filesystem/validate-repo", {
          params: { query: { path: "/repo/subdir" } },
          signal: expect.any(AbortSignal),
        });
        expect(mocks.post).toHaveBeenCalledWith("/projects", {
          body: { local_path: "/repo" },
          signal: expect.any(AbortSignal),
        });
      }),
    );

    it.effect("uses fleet routes when registering on a host", () =>
      Effect.gen(function* () {
        mocks.get.mockResolvedValue({
          data: { is_valid: true, root_path: "/srv/repo" },
          response: new Response(),
        });
        mocks.post.mockResolvedValue({
          data: project("prj_remote"),
          response: new Response(),
        });

        const result = yield* registerExistingProject("/srv/repo/pkg", { hostKey: "epyc" });

        expect(result).toMatchObject({ id: "prj_remote" });
        expect(mocks.get).toHaveBeenCalledWith("/fleet/hosts/{host_key}/filesystem/validate-repo", {
          params: {
            path: { host_key: "epyc" },
            query: { path: "/srv/repo/pkg" },
          },
          signal: expect.any(AbortSignal),
        });
        expect(mocks.post).toHaveBeenCalledWith("/fleet/hosts/{host_key}/projects", {
          params: { path: { host_key: "epyc" } },
          body: { local_path: "/srv/repo" },
          signal: expect.any(AbortSignal),
        });
      }),
    );

    it.effect("rejects invalid repository paths before registering", () =>
      Effect.gen(function* () {
        mocks.get.mockResolvedValue({
          data: { is_valid: false, message: "Not a git repository" },
          response: new Response(),
        });

        const message = yield* registerExistingProject("/tmp").pipe(
          Effect.match({
            onFailure: projectIntakeFailureMessage,
            onSuccess: () => "unexpected success",
          }),
        );

        expect(message).toBe("Not a git repository");
        expect(mocks.post).not.toHaveBeenCalled();
      }),
    );

    it.effect("posts clone body with an optional branch", () =>
      Effect.gen(function* () {
        mocks.post.mockResolvedValue({
          data: project("prj_clone"),
          response: new Response(),
        });

        const result = yield* cloneProject(" git@github.com:octo/repo.git ", " /tmp/repo ", " main ");

        expect(result).toMatchObject({ id: "prj_clone" });
        expect(mocks.post).toHaveBeenCalledWith("/projects/clone", {
          body: {
            url: "git@github.com:octo/repo.git",
            path: "/tmp/repo",
            branch: "main",
          },
          signal: expect.any(AbortSignal),
        });
      }),
    );

    it.effect("uses the fleet clone route when cloning on a host", () =>
      Effect.gen(function* () {
        mocks.post.mockResolvedValue({
          data: project("prj_remote_clone"),
          response: new Response(),
        });

        const result = yield* cloneProject("git@github.com:octo/repo.git", "/srv/repo", undefined, { hostKey: "epyc" });

        expect(result).toMatchObject({ id: "prj_remote_clone" });
        expect(mocks.post).toHaveBeenCalledWith("/fleet/hosts/{host_key}/projects/clone", {
          params: { path: { host_key: "epyc" } },
          body: {
            url: "git@github.com:octo/repo.git",
            path: "/srv/repo",
          },
          signal: expect.any(AbortSignal),
        });
      }),
    );

    it.effect("normalizes a null repository list", () =>
      Effect.gen(function* () {
        mocks.get.mockResolvedValue({
          data: { repositories: null },
          response: new Response(),
        });

        expect(yield* listUserRepositories()).toEqual([]);
      }),
    );
    it.effect("keeps the requested provider host on every discovered repository", () =>
      Effect.gen(function* () {
        mocks.get.mockResolvedValue({
          data: {
            repositories: [
              {
                name_with_owner: "acme/forge",
                ssh_url: "git@ghe.example.com:acme/forge.git",
                default_branch: "main",
              },
            ],
          },
          error: undefined,
        });

        expect(yield* listUserRepositories({ provider: "github", platformHost: "ghe.example.com" })).toEqual([
          {
            name_with_owner: "acme/forge",
            ssh_url: "git@ghe.example.com:acme/forge.git",
            default_branch: "main",
            provider: "github",
            platform_host: "ghe.example.com",
          },
        ]);
        expect(mocks.get).toHaveBeenCalledWith("/platform/user-repositories", {
          params: {
            query: {
              provider: "github",
              platform_host: "ghe.example.com",
              limit: 1000,
            },
          },
          signal: expect.any(AbortSignal),
        });
      }),
    );
  });
});
