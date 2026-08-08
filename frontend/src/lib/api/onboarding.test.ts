import { beforeEach, describe, expect, it, vi } from "vite-plus/test";
import type { PullRequest } from "./types.js";

const runtime = vi.hoisted(() => ({
  post: vi.fn(),
}));

vi.mock("./runtime.ts", () => ({
  client: { POST: runtime.post },
  apiErrorMessage: (error: { detail?: string } | undefined, fallback: string) => error?.detail ?? fallback,
}));

import { createPullRequestWorkspace } from "./onboarding.ts";

describe("onboarding API", () => {
  beforeEach(() => runtime.post.mockReset());

  it("creates a workspace with the pull request's full provider identity", async () => {
    runtime.post.mockResolvedValue({
      data: { id: "ws-42", status: "provisioning" },
      error: undefined,
    });
    const pull = {
      Number: 42,
      repo: {
        provider: "github",
        platform_host: "ghe.example.com",
        owner: "acme",
        name: "forge",
        repo_path: "acme/forge",
      },
    } as PullRequest;

    await expect(createPullRequestWorkspace(pull)).resolves.toEqual({
      id: "ws-42",
      status: "provisioning",
    });
    expect(runtime.post).toHaveBeenCalledWith("/workspaces", {
      body: {
        provider: "github",
        platform_host: "ghe.example.com",
        owner: "acme",
        name: "forge",
        mr_number: 42,
      },
    });
  });
});
