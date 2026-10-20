import { Effect } from "effect";
import type { AppServices } from "../app/runtime.js";
import {
  dismissWorklist,
  getWorklist,
  isWorklistNotFound,
  listWorklists,
  setWorklistItemsHandled,
  undismissWorklist,
  worklistFailureMessage,
  type Worklist,
  type WorklistFailure,
  type WorklistItem,
  type WorklistMarks,
  type WorklistSummary,
} from "../api/worklists.js";
import { worklistMarkMatchesItem } from "../components/worklists/worklist-items.js";

export type WorklistDetailError = { readonly kind: "notFound" } | { readonly kind: "failed"; readonly message: string };

export interface WorklistsStore {
  getSummaries(): readonly WorklistSummary[];
  /** Whether a summaries read has ever succeeded. */
  summariesLoaded(): boolean;
  /** The latest summaries read's failure, cleared by the next success. */
  getSummariesError(): string | null;
  getOpenId(): number | null;
  getDetail(): Worklist | null;
  isDetailLoading(): boolean;
  getDetailError(): WorklistDetailError | null;
  // Refreshes record their outcome in the store and still fail with the read's
  // failure, so callers can observe a hub outage.
  refreshSummariesEffect(): Effect.Effect<void, WorklistFailure, AppServices>;
  openEffect(id: number | null): Effect.Effect<void, never, AppServices>;
  refreshOpenDetailEffect(): Effect.Effect<void, WorklistFailure, AppServices>;
  /**
   * Reconciles the open list on the lightweight poll path. A read of the open
   * list already in flight answers for it, so a slow response is not stacked.
   */
  reconcileOpenDetailEffect(): Effect.Effect<void, WorklistFailure, AppServices>;
  refreshAllEffect(): Effect.Effect<void, WorklistFailure, AppServices>;
  dismissEffect(id: number): Effect.Effect<WorklistSummary, WorklistFailure, AppServices>;
  undismissEffect(id: number): Effect.Effect<WorklistSummary, WorklistFailure, AppServices>;
  /** Marks or unmarks items and applies the server's marks to the loaded list. */
  setHandledEffect(
    id: number,
    items: readonly WorklistItem[],
    handled: boolean,
  ): Effect.Effect<WorklistMarks, WorklistFailure, AppServices>;
}

export interface WorklistsStoreOptions {
  /** Observes every failed list or summaries read, whoever started it. */
  readonly onReadFailure?: (failure: WorklistFailure) => void;
}

class WorklistsStoreImpl implements WorklistsStore {
  readonly #onReadFailure: (failure: WorklistFailure) => void;
  #summaries = $state.raw<WorklistSummary[]>([]);
  #summariesLoaded = $state(false);
  #summariesError = $state<string | null>(null);
  #openId = $state<number | null>(null);
  #detail = $state.raw<Worklist | null>(null);
  #detailLoading = $state(false);
  #detailError = $state.raw<WorklistDetailError | null>(null);
  #summariesGeneration = 0;
  #detailGeneration = 0;

  constructor(options: WorklistsStoreOptions) {
    this.#onReadFailure = options.onReadFailure ?? (() => {});
  }

  getSummaries(): readonly WorklistSummary[] {
    return this.#summaries;
  }

  summariesLoaded(): boolean {
    return this.#summariesLoaded;
  }

  getSummariesError(): string | null {
    return this.#summariesError;
  }

  getOpenId(): number | null {
    return this.#openId;
  }

  getDetail(): Worklist | null {
    return this.#detail?.id === this.#openId ? this.#detail : null;
  }

  isDetailLoading(): boolean {
    return this.#detailLoading;
  }

  getDetailError(): WorklistDetailError | null {
    return this.#detailError;
  }

  refreshSummariesEffect(): Effect.Effect<void, WorklistFailure, AppServices> {
    return this.#readSummaries().pipe(Effect.flatMap(failIfPresent));
  }

  openEffect(id: number | null): Effect.Effect<void, never, AppServices> {
    return Effect.suspend(() => {
      if (id === this.#openId && (this.#detail?.id === id || this.#detailLoading)) return Effect.void;
      this.#openId = id;
      this.#detailError = null;
      if (id === null) {
        this.#detailGeneration++;
        this.#detailLoading = false;
        return Effect.void;
      }
      return this.#readDetail(id).pipe(Effect.asVoid);
    });
  }

  refreshOpenDetailEffect(): Effect.Effect<void, WorklistFailure, AppServices> {
    return this.#readOpenDetail().pipe(Effect.flatMap(failIfPresent));
  }

  reconcileOpenDetailEffect(): Effect.Effect<void, WorklistFailure, AppServices> {
    return Effect.suspend(() =>
      this.#detailLoading && this.#openId !== null ? Effect.void : this.refreshOpenDetailEffect(),
    );
  }

  refreshAllEffect(): Effect.Effect<void, WorklistFailure, AppServices> {
    return Effect.all([this.#readSummaries(), this.#readOpenDetail()], { concurrency: "unbounded" }).pipe(
      Effect.flatMap(([summariesFailure, detailFailure]) => failIfPresent(summariesFailure ?? detailFailure)),
    );
  }

  #readSummaries(): Effect.Effect<WorklistFailure | null, never, AppServices> {
    return Effect.suspend(() => {
      const generation = ++this.#summariesGeneration;
      return listWorklists().pipe(
        Effect.match({
          onSuccess: (summaries) => {
            if (generation !== this.#summariesGeneration) return null;
            this.#summaries = summaries.filter((summary) => summary.dismissed_at === null);
            this.#summariesError = null;
            this.#summariesLoaded = true;
            return null;
          },
          onFailure: (failure) => {
            this.#onReadFailure(failure);
            if (generation === this.#summariesGeneration) {
              this.#summariesError = worklistFailureMessage(failure, "Could not load worklists");
            }
            return failure;
          },
        }),
      );
    });
  }

  #readOpenDetail(): Effect.Effect<WorklistFailure | null, never, AppServices> {
    return Effect.suspend(() => (this.#openId === null ? Effect.succeed(null) : this.#readDetail(this.#openId)));
  }

  // A missing worklist is a result, not a failure: the view falls back to
  // another list.
  #readDetail(id: number): Effect.Effect<WorklistFailure | null, never, AppServices> {
    const generation = ++this.#detailGeneration;
    this.#detailLoading = true;
    return getWorklist(id).pipe(
      Effect.match({
        onSuccess: (detail) => {
          if (generation !== this.#detailGeneration) return null;
          this.#detail = detail;
          this.#detailError = null;
          this.#detailLoading = false;
          return null;
        },
        onFailure: (failure) => {
          if (isWorklistNotFound(failure)) {
            if (generation !== this.#detailGeneration) return null;
            this.#detailLoading = false;
            this.#detail = null;
            this.#detailError = { kind: "notFound" };
            this.#summaries = this.#summaries.filter((summary) => summary.id !== id);
            return null;
          }
          this.#onReadFailure(failure);
          if (generation !== this.#detailGeneration) return failure;
          this.#detailLoading = false;
          if (this.#detail?.id === id) return failure;
          this.#detailError = {
            kind: "failed",
            message: worklistFailureMessage(failure, "Could not load the worklist"),
          };
          return failure;
        },
      }),
      Effect.onInterrupt(() =>
        Effect.sync(() => {
          if (generation === this.#detailGeneration) this.#detailLoading = false;
        }),
      ),
    );
  }

  dismissEffect(id: number): Effect.Effect<WorklistSummary, WorklistFailure, AppServices> {
    return dismissWorklist(id).pipe(
      Effect.tap(() =>
        Effect.sync(() => {
          this.#summariesGeneration++;
          this.#summaries = this.#summaries.filter((summary) => summary.id !== id);
        }),
      ),
    );
  }

  undismissEffect(id: number): Effect.Effect<WorklistSummary, WorklistFailure, AppServices> {
    return undismissWorklist(id).pipe(Effect.tap(() => this.#readSummaries()));
  }

  setHandledEffect(
    id: number,
    items: readonly WorklistItem[],
    handled: boolean,
  ): Effect.Effect<WorklistMarks, WorklistFailure, AppServices> {
    return setWorklistItemsHandled(id, items, handled).pipe(
      Effect.tap((marks) => Effect.sync(() => this.#applyMarks(id, marks))),
    );
  }

  #applyMarks(id: number, marks: WorklistMarks): void {
    this.#summaries = this.#summaries.map((summary) => (summary.id === id ? marks.worklist : summary));
    const detail = this.#detail;
    if (detail?.id !== id) return;
    this.#detail = {
      ...detail,
      items: detail.items.map((item) => {
        const mark = marks.items.find((candidate) => worklistMarkMatchesItem(candidate, item));
        return mark ? { ...item, handled_at: mark.handled_at } : item;
      }),
    };
  }
}

function failIfPresent(failure: WorklistFailure | null): Effect.Effect<void, WorklistFailure> {
  return failure === null ? Effect.void : Effect.fail(failure);
}

export function createWorklistsStore(options: WorklistsStoreOptions = {}): WorklistsStore {
  return new WorklistsStoreImpl(options);
}
