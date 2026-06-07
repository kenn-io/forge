import { cleanup, render, screen } from "@testing-library/svelte";
import { afterEach, describe, expect, it } from "vite-plus/test";

import JobRow from "./JobRow.svelte";
import type { components } from "../../api/roborev/generated/schema.js";

type ReviewJob = components["schemas"]["ReviewJob"];

function makeJob(tokenUsage?: string): ReviewJob {
  return {
    id: 42,
    agent: "codex",
    agentic: false,
    enqueued_at: "2026-04-10T12:00:00Z",
    finished_at: "2026-04-10T12:08:00Z",
    git_ref: "abcdef123456",
    job_type: "review",
    prompt_prebuilt: false,
    repo_id: 1,
    retry_count: 0,
    started_at: "2026-04-10T12:03:00Z",
    status: "done",
    token_usage: tokenUsage,
  };
}

describe("JobRow", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders priced roborev token usage as an estimated cost", () => {
    render(JobRow, {
      props: {
        job: makeJob('{"total_output_tokens":28800,"peak_context_tokens":118000,"cost_usd":0.42,"has_cost":true}'),
        selected: false,
        highlighted: false,
        onclick: () => {},
      },
    });

    expect(screen.getByText("~$0.42")).toBeTruthy();
  });
});
