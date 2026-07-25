# Gitealike staged review hydration

## Goal

Make Forgejo and Gitea archive hydration usable without fabricated provider
rate state, while ensuring the bounded review-comment fan-out can complete on
low valid sync budgets without crossing the live-work reserve.

## Rate-budget authority

Forgejo and Gitea transports will continue counting every wire request. They
will also consume provider rate-limit headers when a response supplies a
complete limit, remaining, and reset tuple.

When either provider supplies no usable reset time, archive admission will use
the configured local sync budget as its authority. Local fallback availability
is the unspent hourly budget above the live reserve; it does not synthesize
provider quota facts or change GitHub and GitLab's provider-signal requirement.
The rate tracker's clock-hour rollover remains responsible for resetting the
local budget and waking deferred archive work.

The Gitealike container test will remove its direct `UpdateFromRate` call. Its
archive assertions must therefore pass using state produced by the same
transport and admission path as production.

## Canonical staged review path

Resumable review hydration belongs in the canonical merge-request detail path,
not in the archive service. Live refresh and archive hydration will call the
same provider-neutral staging coordinator.

Gitealike clients will expose a paged review-hydration interface with two
bounded operations:

1. list review identities through cycle-detected pagination;
2. read the inline comments for one review identity.

Review discovery accepts at most ten pages and 100 reviews. The coordinator
will persist a stage keyed by merge request, provider updated time, head SHA,
and generation. The stage records the ordered review identities, the next
review to hydrate, and normalized staged threads. Each detail pass either
completes review discovery or hydrates at most eight reviews. It never exposes
a partial stage.

When the stage completes, one revision-fenced transaction replaces the live
review-thread and review-comment event datasets and deletes the stage. Until
that transaction commits, readers continue seeing the previous complete
dataset. A changed provider updated time or head SHA invalidates the old stage
and starts a new generation; the final swap is additionally fenced to the
current local parent revision from that pass. Authentication, provider,
cancellation, and page-limit failures leave both the previous live dataset and
resumable stage intact.

Providers without the paged interface keep the existing complete-reader path.
No archive-specific provider reader, normalizer, or child writer will be added.

## Admission cost

`ArchiveItemSyncCost` will use provider kind and item type. Forgejo and Gitea
merge-request cost will reserve 38 wire attempts: twice the existing nine-call
detail base plus the ten-page discovery maximum. An eight-review continuation
fits below the same bound. The cost includes authentication retry headroom but
does not reserve the entire 100-review dataset at once.

The 38-attempt pass fits the 39 requests available above the existing live
floor at the minimum valid hourly budget of 50. The transport allowance remains
the hard wire-attempt ceiling for that pass. Exhausting it defers the stage
without discarding progress or marking detail fetched.

## Persistence

The change will add one migration containing the provider-neutral stage and
staged-thread tables. Stage rows cascade with their merge request. Live review
tables remain unchanged, so API reads and existing review-thread queries never
need a staged/live compatibility branch.

No legacy archive dataset engine will be restored. Existing archive lookup
progress continues to track the item as a whole; staged child progress is
owned by canonical detail synchronization.

## Verification

Focused tests will prove:

- Gitealike rate responses update real tracker state when headers exist;
- headerless Gitealike admission uses only the configured local budget and
  still preserves the live floor;
- a dataset requiring more than the old one-shot allowance completes across
  multiple canonical detail passes;
- partial and failed stages leave the prior complete dataset visible;
- a changed provider snapshot invalidates staged work before final commit;
- the provider-aware stage-pass cost matches its hard wire allowance;
- the container fixture performs archive hydration without manually seeding
  rate state.

Forgejo and Gitea container commands will run concurrently. If the existing
Gitea 1.24.6 SDK timeline-label decode defect still prevents its full fixture,
the result will be reported separately rather than hidden by synthetic rate
state or a compatibility decoder.

## Non-goals

- Adding an archive-only review sync engine.
- Exposing partial review stages through API or UI reads.
- Synthesizing provider quota or reset headers.
- Adding a Gitea timeline compatibility decoder.
- Changing review support or archive coverage claims for other providers.
