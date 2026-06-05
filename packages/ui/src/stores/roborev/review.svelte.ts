import type { RoborevClient } from "../../api/roborev/client.js";
import type { components, operations } from "../../api/roborev/generated/schema.js";

type Review = components["schemas"]["Review"];
type ReviewJob = components["schemas"]["ReviewJob"];
type ReviewResponse = components["schemas"]["Response"];
type ListJobsQuery = NonNullable<operations["list-jobs"]["parameters"]["query"]>;

export interface ReviewStoreOptions {
  client: RoborevClient;
  onError?: (msg: string) => void;
}

export function createReviewStore(opts: ReviewStoreOptions) {
  const client = opts.client;

  // State
  let review = $state<Review | null>(null);
  let selectedJob = $state<ReviewJob | null>(null);
  let responses = $state<ReviewResponse[]>([]);
  let loading = $state(false);
  let selectedJobId = $state<number | undefined>(undefined);
  let reviewNotFound = $state(false);
  let storeError = $state<string | null>(null);
  let requestVersion = 0;

  async function loadReview(jobId: number): Promise<void> {
    const version = ++requestVersion;
    loading = true;
    reviewNotFound = false;
    storeError = null;
    review = null;
    selectedJob = null;
    responses = [];
    try {
      // Fetch review+comments and job metadata in
      // parallel. Use allSettled for the job fetch so a
      // transport failure there doesn't drop valid
      // review/comments data.
      const [reviewResult, commentsResult] = await Promise.all([
        client.GET("/api/review", {
          params: { query: { job_id: jobId } },
        }),
        client.GET("/api/comments", {
          params: { query: { job_id: jobId } },
        }),
      ]);

      const jobSettled = await Promise.allSettled([
        client.GET("/api/jobs", {
          params: {
            query: { id: jobId, limit: 1 } satisfies ListJobsQuery,
          },
        }),
      ]);

      if (version !== requestVersion) return;

      // Extract job metadata: prefer standalone fetch,
      // fall back to the review's nested job object.
      if (jobSettled[0]?.status === "fulfilled" && !jobSettled[0].value.error && jobSettled[0].value.data?.jobs?.[0]) {
        selectedJob = jobSettled[0].value.data.jobs[0];
      }

      if (reviewResult.error) {
        // Distinguish 404 (job still running, no review)
        // from other errors (proxy/daemon failures).
        if (reviewResult.response?.status === 404) {
          review = null;
          reviewNotFound = true;
        } else {
          storeError = "Failed to load review";
          opts.onError?.("Failed to load review");
        }
      } else {
        review = reviewResult.data ?? null;
        reviewNotFound = false;
        // Fall back to review's nested job if standalone
        // fetch failed (transport error, etc.)
        if (!selectedJob && review?.job) {
          selectedJob = review.job;
        }
      }

      if (!commentsResult.error && commentsResult.data) {
        responses = commentsResult.data.responses ?? [];
      }
    } catch {
      if (version !== requestVersion) return;
      storeError = "Failed to load review";
      opts.onError?.("Failed to load review");
    } finally {
      if (version === requestVersion) loading = false;
    }
  }

  async function closeReview(jobId: number): Promise<void> {
    const closed = !(review?.closed ?? false);
    const { error } = await client.POST("/api/review/close", {
      body: { job_id: jobId, closed },
    });
    if (error) {
      opts.onError?.("Failed to close review");
      return;
    }
    if (review) {
      review = { ...review, closed };
    }
  }

  async function addComment(jobId: number, text: string): Promise<boolean> {
    const { data, error } = await client.POST("/api/comment", {
      body: {
        job_id: jobId,
        commenter: "web",
        comment: text,
      },
    });
    if (error || !data) {
      opts.onError?.("Failed to add comment");
      return false;
    }
    responses = [...responses, data];
    return true;
  }

  function setSelectedJobId(jobId: number | undefined): void {
    selectedJobId = jobId;
    if (jobId !== undefined) {
      void loadReview(jobId);
    } else {
      review = null;
      selectedJob = null;
      responses = [];
      reviewNotFound = false;
    }
  }

  // Getters
  function getReview(): Review | null {
    return review;
  }
  function getResponses(): ReviewResponse[] {
    return responses;
  }
  function isLoading(): boolean {
    return loading;
  }
  function getSelectedJobId(): number | undefined {
    return selectedJobId;
  }
  function isReviewNotFound(): boolean {
    return reviewNotFound;
  }
  function getPrompt(): string {
    return review?.prompt ?? "";
  }
  function getOutput(): string {
    return review?.output ?? "";
  }
  function isClosed(): boolean {
    return review?.closed ?? false;
  }
  function getError(): string | null {
    return storeError;
  }
  function getSelectedJob(): ReviewJob | null {
    return selectedJob;
  }

  return {
    getReview,
    getSelectedJob,
    getResponses,
    isLoading,
    getSelectedJobId,
    isReviewNotFound,
    getError,
    getPrompt,
    getOutput,
    isClosed,
    loadReview,
    closeReview,
    addComment,
    setSelectedJobId,
  };
}

export type ReviewStore = ReturnType<typeof createReviewStore>;
