import * as roborevAPI from "../../api/roborev/generated/client.js";
import { Clipboard } from "@effect/platform-browser";
import { Effect, Option } from "effect";
import type { AppRuntime } from "../../app/runtime.js";
import { executeRoborevRequest, type RoborevClient } from "../../api/roborev/client.js";
import type * as RoborevModels from "../../api/roborev/generated/models/index.js";
import {
  RoborevMutationError,
  roborevMutationFailureMessage,
  RoborevResponseError,
  RoborevWorkflow,
} from "./roborev-workflow.js";

type Review = RoborevModels.Review;
type ReviewJob = RoborevModels.ReviewJob;
type ReviewResponse = RoborevModels.Response;
type ListJobsQuery = NonNullable<RoborevModels.ListJobsParams>;

interface ReviewAuthority {
  readonly review: Review | null;
  readonly selectedJob: ReviewJob | null;
  readonly responses: ReadonlyArray<ReviewResponse>;
  readonly reviewNotFound: boolean;
}

export interface ReviewStoreOptions {
  client: RoborevClient;
  runtime: AppRuntime;
  owner: string;
  onError?: (msg: string) => void;
}

export function createReviewStore(opts: ReviewStoreOptions) {
  const client = opts.client;

  // State
  let review = $state.raw<Review | null>(null);
  let selectedJob = $state.raw<ReviewJob | null>(null);
  let responses = $state.raw<ReviewResponse[]>([]);
  let loading = $state(false);
  let selectedJobId = $state<number | undefined>(undefined);
  let reviewNotFound = $state(false);
  let storeError = $state<string | null>(null);

  const fetchReviewAuthority = Effect.fn("RoborevReview.fetchAuthority")(function* (jobId: number) {
    const result = yield* Effect.all(
      {
        review: executeRoborevRequest("GET Roborev review", (signal) =>
          roborevAPI.getReview({ job_id: jobId }, { signal }, client),
        ),
        comments: executeRoborevRequest("GET Roborev review comments", (signal) =>
          roborevAPI.listComments({ job_id: jobId }, { signal }, client),
        ),
        job: executeRoborevRequest("GET Roborev review job", (signal) =>
          roborevAPI.listJobs({ id: jobId, limit: 1 } satisfies ListJobsQuery, { signal }, client),
        ).pipe(Effect.option),
      },
      { concurrency: "unbounded" },
    );

    const notFound = result.review.status === 404;
    if (result.review.status !== 200 && !notFound) {
      return yield* Effect.fail(
        RoborevResponseError.make({
          operation: "GET Roborev review",
          message: "Failed to load review",
          cause: result.review.data,
        }),
      );
    }
    if (result.comments.status !== 200) {
      return yield* Effect.fail(
        RoborevResponseError.make({
          operation: "GET Roborev review comments",
          message: "Failed to load review comments",
          cause: result.comments.data,
        }),
      );
    }

    const fetchedReview = result.review.status === 200 ? result.review.data : null;
    const fetchedJob =
      Option.isSome(result.job) && result.job.value.status === 200
        ? (result.job.value.data?.jobs?.[0] ?? fetchedReview?.job ?? null)
        : (fetchedReview?.job ?? null);
    return {
      review: fetchedReview,
      selectedJob: fetchedJob,
      responses: result.comments.data?.responses ?? [],
      reviewNotFound: notFound,
    } satisfies ReviewAuthority;
  });

  const publishReviewAuthority = (jobId: number, authority: ReviewAuthority) =>
    Effect.sync(() => {
      if (selectedJobId !== jobId) return;
      review = authority.review;
      selectedJob = authority.selectedJob;
      responses = [...authority.responses];
      reviewNotFound = authority.reviewNotFound;
      storeError = null;
    });

  const reconcileReview = (jobId: number) =>
    fetchReviewAuthority(jobId).pipe(Effect.tap((authority) => publishReviewAuthority(jobId, authority)));

  const loadReviewEffect = (jobId: number) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      return yield* workflow.review(
        opts.owner,
        jobId,
        Effect.gen(function* () {
          yield* Effect.sync(() => {
            loading = true;
            reviewNotFound = false;
            storeError = null;
            review = null;
            selectedJob = null;
            responses = [];
          });
          const authority = yield* fetchReviewAuthority(jobId);
          yield* publishReviewAuthority(jobId, authority);
        }).pipe(
          Effect.catch(() =>
            Effect.sync(() => {
              if (selectedJobId !== jobId) return;
              storeError = "Failed to load review";
              opts.onError?.("Failed to load review");
            }),
          ),
          Effect.ensuring(
            Effect.sync(() => {
              if (selectedJobId !== undefined && selectedJobId !== jobId) return;
              loading = false;
            }),
          ),
        ),
      );
    });

  function loadReview(jobId: number): void {
    opts.runtime.runCommand(loadReviewEffect(jobId), {
      operation: "load Roborev review",
      safeContext: { job_id: jobId, owner: opts.owner },
      onFailure: () => opts.onError?.("Failed to load review"),
    });
  }

  const closeReviewEffect = (jobId: number, closed: boolean) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      return yield* workflow.mutate({
        key: `review:${jobId}`,
        operation: "close Roborev review",
        mutation: executeRoborevRequest("close Roborev review", (signal) =>
          roborevAPI.closeReview({ job_id: jobId, closed }, { signal }, client),
        ).pipe(
          Effect.flatMap((result) =>
            result.status !== 200
              ? Effect.fail(RoborevMutationError.make({ operation: "close Roborev review", cause: result.data }))
              : Effect.succeed(closed),
          ),
        ),
        reconcile: (acknowledged) =>
          reconcileReview(jobId).pipe(
            Effect.map((authority) =>
              authority.review?.closed === closed
                ? Option.some(Option.getOrElse(acknowledged, () => closed))
                : Option.none<boolean>(),
            ),
          ),
        onAcknowledgedRefreshFailure: () =>
          Effect.sync(() => opts.onError?.("Review state changed, but the refreshed review is unavailable")),
      });
    });

  function closeReview(jobId: number): void {
    if (selectedJobId !== jobId || review?.job_id !== jobId) {
      opts.onError?.("Failed to close review");
      return;
    }
    const closed = !review.closed;
    opts.runtime.runCommand(closeReviewEffect(jobId, closed), {
      operation: "close Roborev review",
      safeContext: { job_id: jobId, owner: opts.owner },
      onFailure: (failure) => opts.onError?.(roborevMutationFailureMessage("Failed to close review", failure)),
    });
  }

  const addCommentEffect = (jobId: number, text: string, baselineResponseIDs: ReadonlySet<number>) =>
    Effect.gen(function* () {
      const workflow = yield* RoborevWorkflow;
      const response = yield* workflow.mutate({
        key: `review:${jobId}`,
        operation: "add Roborev comment",
        mutation: executeRoborevRequest("add Roborev comment", (signal) =>
          roborevAPI.addComment(
            {
              job_id: jobId,
              commenter: "web",
              comment: text,
            },
            { signal },
            client,
          ),
        ).pipe(
          Effect.flatMap((result) =>
            result.status !== 201
              ? Effect.fail(
                  RoborevMutationError.make({
                    operation: "add Roborev comment",
                    cause: result.data ?? new Error("Roborev comment response was empty"),
                  }),
                )
              : Effect.succeed(result.data),
          ),
        ),
        reconcile: (acknowledged) =>
          reconcileReview(jobId).pipe(
            Effect.map((authority) => {
              const accepted = authority.responses.find(
                (candidate) =>
                  !baselineResponseIDs.has(candidate.id) &&
                  candidate.responder === "web" &&
                  candidate.response === text &&
                  (candidate.job_id === undefined || candidate.job_id === jobId),
              );
              return accepted === undefined
                ? Option.none<ReviewResponse>()
                : Option.some(Option.getOrElse(acknowledged, () => accepted));
            }),
          ),
        onAcknowledgedRefreshFailure: () =>
          Effect.sync(() => opts.onError?.("Comment was added, but the refreshed review is unavailable")),
      });
      yield* Effect.sync(() => {
        if (selectedJobId !== jobId || responses.some((candidate) => candidate.id === response.id)) return;
        responses = [...responses, response];
      });
      return response;
    });

  function addComment(
    jobId: number,
    text: string,
    callbacks?: { readonly onSuccess?: () => void; readonly onFailure?: () => void },
  ): void {
    const baselineResponseIDs = new Set(responses.map((response) => response.id));
    opts.runtime.runCommand(
      addCommentEffect(jobId, text, baselineResponseIDs).pipe(
        Effect.tap(() => Effect.sync(() => callbacks?.onSuccess?.())),
      ),
      {
        operation: "add Roborev comment",
        safeContext: { job_id: jobId, owner: opts.owner },
        onFailure: (failure) => {
          opts.onError?.(roborevMutationFailureMessage("Failed to add comment", failure));
          callbacks?.onFailure?.();
        },
      },
    );
  }

  const copyOutputEffect = (output: string) =>
    Effect.gen(function* () {
      const clipboard = yield* Clipboard.Clipboard;
      yield* clipboard.writeString(output);
    });

  function copyOutput(): void {
    opts.runtime.runCommand(copyOutputEffect(review?.output ?? ""), {
      operation: "copy Roborev review output",
      safeContext: { owner: opts.owner },
      onFailure: () => {},
    });
  }

  function setSelectedJobId(jobId: number | undefined): void {
    selectedJobId = jobId;
    if (jobId === undefined) {
      review = null;
      selectedJob = null;
      responses = [];
      reviewNotFound = false;
      opts.runtime.runCommand(
        Effect.gen(function* () {
          const workflow = yield* RoborevWorkflow;
          yield* workflow.stopReview(opts.owner);
        }),
        {
          operation: "stop Roborev review load",
          safeContext: { owner: opts.owner },
          onFailure: () => {},
        },
      );
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
    loadReviewEffect,
    closeReview,
    closeReviewEffect,
    addComment,
    addCommentEffect,
    copyOutput,
    copyOutputEffect,
    setSelectedJobId,
  };
}

export type ReviewStore = ReturnType<typeof createReviewStore>;
