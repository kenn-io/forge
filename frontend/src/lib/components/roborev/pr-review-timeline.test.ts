import { describe, expect, it } from "vite-plus/test";
import type { PREvent } from "../../api/types.js";
import type * as RoborevModels from "../../api/roborev/generated/models/index.js";
import { buildRoborevTimeline, localReviewEntry, providerReviewEntry } from "./pr-review-timeline.js";

const HEAD = "96027d7aa1b2c3d4e5f60718293a4b5c6d7e8f90";

function comment(id: number, body: string, createdAt = "2026-11-10T18:22:52Z"): PREvent {
  return {
    Author: "roborev-ci[bot]",
    Body: body,
    CreatedAt: createdAt,
    DedupeKey: `comment-${id}`,
    DirectURL: `https://github.com/acme/widgets/pull/7#issuecomment-${id}`,
    EventType: "issue_comment",
    ID: id,
    MergeRequestID: 1,
    MetadataJSON: "",
    PlatformExternalID: String(id),
    PlatformID: id,
    Resolvable: false,
    Resolved: false,
    Summary: "",
    ThreadID: null,
  };
}

function job(overrides: Partial<RoborevModels.ReviewJob>): RoborevModels.ReviewJob {
  return {
    id: 1,
    agent: "codex",
    agentic: false,
    enqueued_at: "2026-11-10T17:00:00Z",
    finished_at: "2026-11-10T17:05:00Z",
    git_ref: HEAD,
    job_type: "review",
    prompt_prebuilt: false,
    repo_id: 1,
    retry_count: 0,
    status: "done",
    ...overrides,
  };
}

describe("providerReviewEntry", () => {
  it("reads a clean combined review", () => {
    const entry = providerReviewEntry(
      comment(
        11,
        "<!-- roborev-pr-comment -->\n## roborev: Combined Review (`180f8e3`)\n\n**Verdict:** No findings at or above medium severity.\n\n---\n*Reviewers: 3x codex | Total: 9m4s*\n",
      ),
      HEAD,
    );
    expect(entry).toMatchObject({
      commit: "180f8e3",
      verdict: "clean",
      findings: 0,
      state: "superseded",
      verdictStated: true,
    });
  });

  it("counts shown and omitted findings", () => {
    const entry = providerReviewEntry(
      comment(
        12,
        "<!-- roborev-pr-comment -->\n## roborev: Combined Review (`96027d7`)\n\n**Verdict:** Changes require fixes for 2 findings.\n\n### High\n\n- `a.go:1`: bad\n\n### Medium\n\n- `b.go:2`: worse\n\n3 more findings in the full review\n",
      ),
      HEAD,
    );
    // The verdict line says 2, so it does not state the total of 5.
    expect(entry).toMatchObject({ verdict: "findings", findings: 5, state: "current", verdictStated: false });
  });

  it("reads a failed review without a finding count", () => {
    const entry = providerReviewEntry(
      comment(13, "<!-- roborev-pr-comment -->\n## roborev: Review Failed (`96027d7`)\n\nAll reviewers failed.\n"),
      HEAD,
    );
    expect(entry).toMatchObject({ verdict: "failed", findings: null, verdictStated: false });
  });

  it("states the verdict when every finding is shown", () => {
    const entry = providerReviewEntry(
      comment(
        17,
        "<!-- roborev-pr-comment -->\n## roborev: Combined Review (`96027d7`)\n\n**Verdict:** Changes require fixes for 1 finding.\n\n### High\n\n- `a.go:1`: bad\n",
      ),
      HEAD,
    );
    expect(entry).toMatchObject({ verdict: "findings", findings: 1, verdictStated: true });
  });

  it("ignores comments without the roborev marker", () => {
    expect(
      providerReviewEntry(
        comment(14, "## roborev: Combined Review (`96027d7`)\n\n**Verdict:** No issues found.\n"),
        HEAD,
      ),
    ).toBeNull();
  });

  it("reads a Bitbucket pull request comment", () => {
    const entry = providerReviewEntry(
      {
        ...comment(
          16,
          "<!-- roborev-pr-comment -->\n## roborev: Combined Review (`96027d7`)\n\n**Verdict:** No issues found.\n",
        ),
        EventType: "comment",
      },
      HEAD,
    );
    expect(entry).toMatchObject({ verdict: "clean", state: "current" });
  });

  it("drops the marker from the rendered body", () => {
    const entry = providerReviewEntry(
      comment(
        15,
        "<!-- roborev-pr-comment -->\n## roborev: Combined Review (`96027d7`)\n\n**Verdict:** No issues found.\n",
      ),
      HEAD,
    );
    expect(entry?.body).toBe("## roborev: Combined Review (`96027d7`)\n\n**Verdict:** No issues found.");
  });
});

describe("localReviewEntry", () => {
  it("marks a closed review addressed and sums its finding counts", () => {
    const entry = localReviewEntry(
      job({
        git_ref: "1111111aaaa",
        verdict: "F",
        closed: true,
        finding_counts: { critical: 0, high: 1, medium: 2, low: 0 },
      }),
      HEAD,
    );
    expect(entry).toMatchObject({ verdict: "findings", findings: 3, state: "addressed" });
  });

  it("uses the end of a range as the reviewed commit", () => {
    const entry = localReviewEntry(job({ job_type: "range", git_ref: "1111111..96027d7aa1b2", verdict: "P" }), HEAD);
    expect(entry).toMatchObject({ commit: "96027d7aa1b2", verdict: "clean", findings: null, state: "current" });
  });

  it("leaves uncommitted reviews without a commit or state", () => {
    const entry = localReviewEntry(job({ job_type: "dirty", git_ref: "dirty", status: "running" }), HEAD);
    expect(entry).toMatchObject({ commit: null, verdict: "running", state: null });
  });

  it("skips jobs that are not reviews", () => {
    expect(localReviewEntry(job({ job_type: "fix" }), HEAD)).toBeNull();
  });
});

describe("buildRoborevTimeline", () => {
  it("orders reviews from both sources oldest first", () => {
    const entries = buildRoborevTimeline(
      [
        comment(
          21,
          "<!-- roborev-pr-comment -->\n## roborev: Combined Review (`96027d7`)\n\n**Verdict:** No issues found.\n",
          "2026-11-10T18:00:00Z",
        ),
        { ...comment(22, "LGTM"), EventType: "issue_comment" },
      ],
      [
        job({ id: 7, finished_at: "2026-11-10T19:00:00Z", verdict: "P" }),
        job({ id: 8, finished_at: "2026-11-10T17:00:00Z", verdict: "F" }),
      ],
      HEAD,
    );
    expect(entries.map((entry) => entry.key)).toEqual(["local:8", "provider:21", "local:7"]);
  });
});
