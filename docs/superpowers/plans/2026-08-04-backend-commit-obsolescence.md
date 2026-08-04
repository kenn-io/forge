# Backend-Authoritative Commit Obsolescence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the frontend force-push lineage replay with sync-time `obsolete` metadata stamped from local-clone head ancestry, per `docs/superpowers/specs/2026-08-03-backend-commit-obsolescence-design.md`.

**Architecture:** The syncer, immediately after diff sync verifies a merge request's current head against the bare clone, marks each stored commit event as obsolete iff its SHA is not an ancestor of that head. The frontend deletes its replay state machine and collapses commit events whose metadata carries `obsolete: true`.

**Tech Stack:** Go (stdlib + testify), SQLite, git subprocess via `internal/gitclone`, Svelte 5 + Vitest, Playwright.

## Global Constraints

- Never use npm; frontend deps via `bun install`, tools via `./node_modules/.bin/vp` (repo root) or `../node_modules/.bin/vp` (from `frontend/`).
- Go tests: `-shuffle=on` when invoking `go test` directly; no `-count=1`; no `-v`; testify `require`/`assert` only (no `t.Fatal`/`t.Error` family); `assert := assert.New(t)` when >3 assertions; table-driven preferred.
- No emojis in code or output. Datetimes UTC.
- Commit every task through the repo commit discipline: invoke `context-sync --commit`, then the `kenn:commit` commit skill. Never amend, never `--no-verify`, conventional subjects explaining WHY, body with context, attribution block ending `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- Fixtures/tests use generic synthetic examples.
- Before any push: full `vp test` Vitest run after the final frontend edit, plus the affected Playwright e2e-full suite (change touches its specs and shared fixtures).
- Svelte edits: use the svelte-code-writer skill tooling when editing `.svelte` files.

---

### Task 1: gitclone ancestry primitives

**Files:**
- Modify: `internal/gitclone/clone.go` (after `MergeBase`, ~line 428)
- Test: `internal/gitclone/ancestry_test.go` (create)

**Interfaces:**
- Consumes: existing `m.git`, `m.ClonePath`, `gitExitCode` (all already in `clone.go`).
- Produces: `func (m *Manager) HasCommit(ctx context.Context, platform, host, owner, name, sha string) (bool, error)` and `func (m *Manager) IsAncestor(ctx context.Context, platform, host, owner, name, ancestor, descendant string) (bool, error)`. Task 3 calls both.

- [ ] **Step 1: Write the failing test**

Create `internal/gitclone/ancestry_test.go`. Build a real repo: base commit `c1`, child `c2` on `main`; orphan-ish side commit `c3` on a branch off `c1`. Clone it bare via `gitclone.New(t.TempDir(), nil)` — follow the pattern in `internal/github/merged_diff_test.go::setupBareClone` (init source repo with `git` CLI, `EnsureClone` or `git clone --bare` into the manager's `ClonePath`). Cases:

```go
func TestIsAncestor(t *testing.T) {
	ctx := context.Background()
	mgr, shas := setupAncestryClone(t) // returns Manager + map[string]string{"c1","c2","c3"}
	assert := assert.New(t)

	ancestor, err := mgr.IsAncestor(ctx, "github", "example.com", "acme", "widgets", shas["c1"], shas["c2"])
	require.NoError(t, err)
	assert.True(ancestor)

	ancestor, err = mgr.IsAncestor(ctx, "github", "example.com", "acme", "widgets", shas["c2"], shas["c1"])
	require.NoError(t, err)
	assert.False(ancestor)

	ancestor, err = mgr.IsAncestor(ctx, "github", "example.com", "acme", "widgets", shas["c3"], shas["c2"])
	require.NoError(t, err)
	assert.False(ancestor)
}

func TestHasCommit(t *testing.T) {
	ctx := context.Background()
	mgr, shas := setupAncestryClone(t)
	assert := assert.New(t)

	has, err := mgr.HasCommit(ctx, "github", "example.com", "acme", "widgets", shas["c1"])
	require.NoError(t, err)
	assert.True(has)

	has, err = mgr.HasCommit(ctx, "github", "example.com", "acme", "widgets", strings.Repeat("d", 40))
	require.NoError(t, err)
	assert.False(has)
}
```

The `setupAncestryClone` helper creates the source repo in `t.TempDir()` with `git init`, `git -c user.email=dev@example.com -c user.name=dev commit --allow-empty -m ...`, captures SHAs with `git rev-parse HEAD`, then bare-clones into `mgr.ClonePath("github", "example.com", "acme", "widgets")` (mkdir parents, `git clone --bare src dest`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -shuffle=on ./internal/gitclone -run 'TestIsAncestor|TestHasCommit'`
Expected: FAIL — `mgr.IsAncestor undefined` / `mgr.HasCommit undefined` (compile error).

- [ ] **Step 3: Implement**

In `internal/gitclone/clone.go` directly after `MergeBase`:

```go
// HasCommit reports whether the clone already contains the commit object.
// A syntactically valid but absent SHA is a clean false, not an error, so
// callers can treat "missing from a head-complete clone" as unreachable.
func (m *Manager) HasCommit(
	ctx context.Context, platform, host, owner, name, sha string,
) (bool, error) {
	clonePath, err := m.ClonePath(platform, host, owner, name)
	if err != nil {
		return false, err
	}
	if _, err := m.git(ctx, clonePath, "cat-file", "-e", sha+"^{commit}"); err != nil {
		if code, ok := gitExitCode(err); ok && code > 0 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// IsAncestor reports whether ancestor is reachable from descendant. Both
// commits must exist in the clone; gate calls with HasCommit.
func (m *Manager) IsAncestor(
	ctx context.Context, platform, host, owner, name, ancestor, descendant string,
) (bool, error) {
	clonePath, err := m.ClonePath(platform, host, owner, name)
	if err != nil {
		return false, err
	}
	if _, err := m.git(ctx, clonePath, "merge-base", "--is-ancestor", ancestor, descendant); err != nil {
		if code, ok := gitExitCode(err); ok && code == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
```

Note on `HasCommit`: `git cat-file -e` exits 1 for a well-formed missing object and 128 for an unresolvable name; both mean "not usable as an ancestor here", hence `code > 0` maps to `(false, nil)`. Process-spawn failures carry no exit code and still surface as errors.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -shuffle=on ./internal/gitclone -run 'TestIsAncestor|TestHasCommit'`
Expected: PASS. Also run the package: `go test -shuffle=on ./internal/gitclone`

- [ ] **Step 5: Commit** (context-sync `--commit`, then commit skill)

Subject: `feat: answer commit reachability from local clones`

---

### Task 2: obsolete metadata read-modify-write helper

**Files:**
- Modify: `internal/github/sync.go` (after `withCommitOrderMetadata`, ~line 50)
- Test: `internal/github/sync_test.go` (append)

**Interfaces:**
- Produces: `func withObsoleteMetadata(metadataJSON string, obsolete bool) (string, bool)` — returns possibly-updated JSON and whether it changed. Task 3 calls it.

- [ ] **Step 1: Write the failing test** (table-driven, in `sync_test.go`)

```go
func TestWithObsoleteMetadata(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		obsolete bool
		want     string
		changed  bool
	}{
		{"set on existing metadata", `{"commit_order_key":3}`, true, `{"commit_order_key":3,"obsolete":true}`, true},
		{"set on empty metadata", ``, true, `{"obsolete":true}`, true},
		{"already set", `{"commit_order_key":3,"obsolete":true}`, true, `{"commit_order_key":3,"obsolete":true}`, false},
		{"clear removes key", `{"commit_order_key":3,"obsolete":true}`, false, `{"commit_order_key":3}`, true},
		{"clear when absent", `{"commit_order_key":3}`, false, `{"commit_order_key":3}`, false},
		{"clear normalizes non-bool garbage", `{"obsolete":"yes"}`, false, `{}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := withObsoleteMetadata(tc.in, tc.obsolete)
			assert.Equal(t, tc.changed, changed)
			if changed {
				assert.JSONEq(t, tc.want, got)
			} else {
				assert.Equal(t, tc.in, got)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -shuffle=on ./internal/github -run TestWithObsoleteMetadata`
Expected: FAIL — `withObsoleteMetadata` undefined.

- [ ] **Step 3: Implement**

```go
// withObsoleteMetadata records whether a commit event's commit is still
// reachable from the merge request head. Unchanged input returns the original
// JSON so callers can skip rewriting untouched rows.
func withObsoleteMetadata(metadataJSON string, obsolete bool) (string, bool) {
	metadata := map[string]any{}
	if metadataJSON != "" {
		var existing map[string]any
		if err := json.Unmarshal([]byte(metadataJSON), &existing); err == nil && existing != nil {
			metadata = existing
		}
	}
	value, present := metadata["obsolete"]
	if obsolete {
		if value == true {
			return metadataJSON, false
		}
		metadata["obsolete"] = true
	} else {
		if !present {
			return metadataJSON, false
		}
		delete(metadata, "obsolete")
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return metadataJSON, false
	}
	return string(encoded), true
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test -shuffle=on ./internal/github -run TestWithObsoleteMetadata`
Expected: PASS.

- [ ] **Step 5: Commit**

Subject: `feat: track commit obsolescence in event metadata`

---

### Task 3: sync-time stamping and wiring

**Files:**
- Modify: `internal/github/sync.go` — new method near `commitOrderAssigner` (~line 133); call sites in `syncOpenMRFromBulk` (after the `UpdateDiffSHAsSnapshot` block ending ~line 7744), `fetchMRDetail` (after its block at ~line 8051), `syncMRDiff` (after `if !applied` at ~line 10829), `syncProviderMRDiff` (after `if !applied` at ~line 10886)
- Modify: `context/github-sync-invariants.md` — add the invariant
- Test: `internal/github/obsolete_stamping_test.go` (create)

**Interfaces:**
- Consumes: Task 1 `HasCommit`/`IsAncestor`, Task 2 `withObsoleteMetadata`, existing `s.db.ListMREvents`, `s.db.UpsertMREvents`, `commitOrderSHA`, `repoHost`, `repoPlatform`.
- Produces: `func (s *Syncer) stampObsoleteCommitEvents(ctx context.Context, repo RepoRef, mrID int64, headSHA string) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/github/obsolete_stamping_test.go` using the harness style of `merged_diff_test.go` (`setupBareClone`, `setupSyncer` — reuse or minimally adapt those helpers; they live in the same package). Build a source repo with:

- base commit `m1` on `main`
- lineage A: `a1`, `a2`, `a3` on branch `feature` (head `a3`)
- lineage B: `b1`, `b2` on `feature-b` branched from `m1`

Bare-clone it, then seed a merge request row plus commit events for `a1..a3` and `b1..b2` via the syncer's db fixture (`DedupeKey` per SHA, `Summary` = full SHA, `MetadataJSON` = `{"commit_order_key":N}`). Scenarios, each calling `syncer.stampObsoleteCommitEvents(ctx, repo, mrID, head)` then asserting via `database.ListMREvents`:

```go
func TestStampObsoleteCommitEventsReplaceAndRestore(t *testing.T) {
	// head = b2: a1..a3 flagged obsolete, b1..b2 unflagged
	// then head = a3: a1..a3 cleared, b1..b2 flagged (alternation)
	// then head = a2: a3 stays flagged, a1..a2 clear (partial restore)
}

func TestStampObsoleteCommitEventsIgnoresBaseAdvance(t *testing.T) {
	// head = a3 with commit events for a1..a3 only; advancing base is
	// irrelevant to ancestry: stamping flags nothing.
}

func TestStampObsoleteCommitEventsSkipsWhenHeadMissing(t *testing.T) {
	// pre-flag a1 obsolete; head SHA not in clone: no rows change.
}

func TestStampObsoleteCommitEventsFlagsShaAbsentFromClone(t *testing.T) {
	// commit event whose SHA never reached the clone (40 hex, absent):
	// flagged obsolete when head verifies.
}

func TestStampObsoleteCommitEventsSkipsNonShaSummaries(t *testing.T) {
	// commit event with non-SHA summary and a non-commit event: untouched.
}
```

Assert flags by parsing `MetadataJSON` per event: `{"commit_order_key":1,"obsolete":true}` vs no `obsolete` key. Assert other metadata keys survive.

- [ ] **Step 2: Run to verify failure**

Run: `go test -shuffle=on ./internal/github -run TestStampObsoleteCommitEvents`
Expected: FAIL — method undefined.

- [ ] **Step 3: Implement the method**

```go
// commitEventSHA returns the event's full commit SHA, or "" when the summary
// is not one. Stamping only trusts full SHAs so clone lookups are exact.
func commitEventSHA(summary string) string {
	sha := commitOrderSHA(summary)
	if len(sha) != 40 && len(sha) != 64 {
		return ""
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return sha
}

// stampObsoleteCommitEvents records on each stored commit event whether its
// commit is still reachable from the merge request's current head, so strict
// date order can collapse superseded commits without replaying force pushes.
// The clone must contain the head (its ancestor closure is then complete);
// otherwise the round is skipped and flags keep their last verified state.
func (s *Syncer) stampObsoleteCommitEvents(
	ctx context.Context, repo RepoRef, mrID int64, headSHA string,
) error {
	if s.clones == nil || headSHA == "" {
		return nil
	}
	platformName := string(repoPlatform(repo))
	host := repoHost(repo)
	hasHead, err := s.clones.HasCommit(ctx, platformName, host, repo.Owner, repo.Name, headSHA)
	if err != nil || !hasHead {
		return err
	}
	events, err := s.db.ListMREvents(ctx, mrID)
	if err != nil {
		return err
	}
	var changed []db.MREvent
	for _, event := range events {
		if event.EventType != "commit" {
			continue
		}
		sha := commitEventSHA(event.Summary)
		if sha == "" {
			continue
		}
		live, err := s.commitReachableFromHead(ctx, platformName, host, repo, sha, headSHA)
		if err != nil {
			return err
		}
		metadataJSON, metadataChanged := withObsoleteMetadata(event.MetadataJSON, !live)
		if !metadataChanged {
			continue
		}
		event.MetadataJSON = metadataJSON
		changed = append(changed, event)
	}
	if len(changed) == 0 {
		return nil
	}
	return s.db.UpsertMREvents(ctx, changed)
}

func (s *Syncer) commitReachableFromHead(
	ctx context.Context, platformName, host string, repo RepoRef, sha, headSHA string,
) (bool, error) {
	if strings.EqualFold(sha, headSHA) {
		return true, nil
	}
	has, err := s.clones.HasCommit(ctx, platformName, host, repo.Owner, repo.Name, sha)
	if err != nil || !has {
		// Absent from a clone that contains the head means unreachable:
		// fetching the head materializes its full ancestor closure.
		return false, err
	}
	return s.clones.IsAncestor(ctx, platformName, host, repo.Owner, repo.Name, sha, headSHA)
}
```

- [ ] **Step 4: Run the stamping tests**

Run: `go test -shuffle=on ./internal/github -run TestStampObsoleteCommitEvents`
Expected: PASS.

- [ ] **Step 5: Wire the four open-MR diff sites**

After each successful diff snapshot (`applied == true` path), add the same non-fatal call. Head variable per site: `headSHA` in `syncOpenMRFromBulk` and `fetchMRDetail`; `normalized.PlatformHeadSHA` in `syncMRDiff` and `syncProviderMRDiff`. Pattern:

```go
if err := s.stampObsoleteCommitEvents(ctx, repo, mrID, headSHA); err != nil {
	slog.Warn("stamp obsolete commit events failed",
		"repo", repo.Owner+"/"+repo.Name,
		"number", number, "err", err,
	)
}
```

Stamping is presentation metadata: it must never fail the sync, hence warn-and-continue. Do not wire `fetchAndUpdateClosed` (closed/merged MRs keep their last stamped state).

Add one wiring test: drive `syncMRDiff` (or `syncProviderMRDiff`, whichever the existing harness reaches with least setup — `merged_diff_test.go::setupSyncer` shows the construction) against the fixture clone with seeded events for lineage A while head is `b2`, and assert the flags landed. This proves stamping is reached through a real sync path, not only by direct method call.

- [ ] **Step 6: Run the package**

Run: `go test -shuffle=on ./internal/github`
Expected: PASS.

- [ ] **Step 7: Record the invariant**

Append to `context/github-sync-invariants.md` (one line, follow the doc's style): commit-event `obsolete` metadata is stamped only from clone-verified head ancestry after diff sync (`internal/github/sync.go::stampObsoleteCommitEvents`); never derive obsolescence from provider commit lists (base movement falsifies them) and never infer it in the frontend.

- [ ] **Step 8: Commit**

Subject: `feat: stamp force-push obsolete commits from clone ancestry`
Body: why provider lists and frontend replay cannot answer reachability; the head-verified guard; skip semantics.

---

### Task 4: frontend collapses the flag, replay machine deleted

**Files:**
- Modify: `packages/ui/src/components/detail/EventTimeline.svelte` (delete `obsoleteCommitOrders` lines ~607–719; edit `collapseObsoleteCommitEntries` ~721; call sites ~817, ~840)
- Test: `packages/ui/src/components/detail/EventTimeline.test.ts`

Use the svelte-code-writer skill tooling for the `.svelte` edit.

- [ ] **Step 1: Rewrite the component tests first**

In `EventTimeline.test.ts`, delete the replay-scenario tests (lines ~1770–2171): "collapses only the rewound-away commits…", "restores every commit from an obsolete lineage…", "restores every ancestor…", "keeps descendants obsolete…", "retires the active lineage after repeated force-push alternation", "preserves unrelated obsolete commits…". Keep all ordering tests (force-push boundary/generation ordering) and keep "expands collapsed obsolete commits on demand in strict date order" (~2172) but change its fixture events to carry the flag. Add two thin cases:

```ts
it("collapses commit events flagged obsolete in strict date order", () => {
  // three commit events; the two with MetadataJSON '{"commit_order_key":N,"obsolete":true}'
  // collapse into one obsolete run; the unflagged commit renders normally.
});

it("ignores the obsolete flag outside commit events and non-boolean values", () => {
  // a comment event with '{"obsolete":true}' renders normally;
  // a commit with '{"obsolete":"yes"}' renders normally.
});
```

Follow the file's existing event-builder helpers for fixtures; flags ride `MetadataJSON`.

- [ ] **Step 2: Run to verify the new tests fail**

Run: `./node_modules/.bin/vp test run --project unit packages/ui/src/components/detail/EventTimeline.test.ts` (adjust to the repo's Vitest invocation for packages/ui if the project filter differs)
Expected: new cases FAIL (flag not consulted yet); deleted cases gone.

- [ ] **Step 3: Implement**

In `EventTimeline.svelte`: delete `obsoleteCommitOrders` entirely (including its comment block). Replace the head of `collapseObsoleteCommitEntries`:

```ts
function isObsoleteCommit(event: PREvent | IssueEvent): boolean {
  return event.EventType === "commit" && parseMetadata(event).obsolete === true;
}

function collapseObsoleteCommitEntries(
  entries: TimelineEntry[],
  keyPrefix: string,
): TimelineEntry[] {
  const collapsed: TimelineEntry[] = [];
  let run: Array<PREvent | IssueEvent> = [];
  // flushRun unchanged
  for (const entry of entries) {
    if (isObsoleteCommit(entry.event)) {
      run = [...run, entry.event];
      continue;
    }
    flushRun();
    collapsed.push(entry);
  }
  flushRun();
  return collapsed;
}
```

Drop the removed `orderingSourceEvents` argument at both call sites (~817 `collapseObsoleteCommitEntries(entries, "")`, ~840 `collapseObsoleteCommitEntries(entries, "compact-")`). The early-return `if (obsoleteOrders.size === 0)` disappears; the loop is already a no-op without flagged commits. Keep the strict-date-only ternary gating at the call sites, keep `buildForcePushBoundaries`/`buildForcePushGenerations`/`buildForcePushDisplaySortKeys` untouched (grouped ordering), and update the comment above the collapse function to say the backend stamps the flag from clone ancestry.

- [ ] **Step 4: Run tests**

Run the file, then the full Vitest suite: `./node_modules/.bin/vp test`
Expected: PASS. Also run `./node_modules/.bin/vp run frontend-check` (types/lint) if that is the repo's check entry point.

- [ ] **Step 5: Commit**

Subject: `fix: collapse force-pushed commits from backend obsolete metadata`
Body: the replay state machine could not derive reachability from head-only force-push events (five review rounds of counterexamples); the backend now stamps ground truth.

---

### Task 5: SQLite-backed fixture and Playwright coverage

**Files:**
- Modify: `internal/testutil/fixtures.go` (widgets PR#6 block, ~lines 803–930)
- Modify: `frontend/tests/e2e-full/pr-timeline-filters.spec.ts` (rewind/restore assertions ~line 100+)

**Interfaces:**
- Consumes: the `obsolete` metadata contract from Tasks 3–4.

- [ ] **Step 1: Update the fixture**

Widgets PR#6 ends restored to `w6OldCommit5` after rewind (`5→3`), replacement (`3→new3`), and restoration (`new3→old5`). Under head `w6OldCommit5`, the old lineage (`w6OldCommit1..5`, `commit_order_key` 1–5) is live and the replacement lineage (`w6NewCommit1..3`, keys 6–8) is unreachable. Add `"obsolete":true` to the three `w6NewCommit*` events only, e.g. `{"commit_order":1,"commit_order_key":6,"obsolete":true}`. Keep the three force-push events and all `commit_order_key` values — grouped ordering still consumes them. Run `go test -shuffle=on ./internal/testutil ./internal/server/apitest` (and any package the fixture change breaks per compile errors).

- [ ] **Step 2: Update the Playwright spec**

In `pr-timeline-filters.spec.ts`, keep the scenario (open widgets PR#6, switch to Strict date order) and assert the final state only: all five old-lineage commit rows visible; the replacement lineage collapsed behind the obsolete-commits disclosure (reuse the spec's existing selectors for collapsed runs); expanding reveals the three replaced commits. Remove any assertions that encoded replay intermediate states. This case proves `obsolete` metadata survives SQLite and the detail API into strict-date rendering; sync-side computation is proven by Task 3's Go tests against real clones — together the chain is covered at the DB seam.

- [ ] **Step 3: Run the affected e2e suite**

Run: `cd frontend && bun run test:e2e` filtered to the affected spec if the runner supports it (`node ./scripts/run-e2e-to-file.ts` is `test:e2e`; check the script for a filter argument — otherwise run the full e2e-full suite as the pre-push rule requires anyway).
Expected: PASS.

- [ ] **Step 4: Full frontend verification** (pre-push rule)

Run: `./node_modules/.bin/vp test` (full Vitest) — must be after the final frontend edit.
Expected: PASS.

- [ ] **Step 5: Commit**

Subject: `test: prove obsolete metadata survives SQLite into strict date order`

---

### Task 6: full verification, PR text, push

**Files:**
- Modify: PR #817 title/body via `gh` (read `context/pull-request-workflow.md` first — it owns PR-metadata rules)

- [ ] **Step 1: Full test sweep**

Run: `go test -shuffle=on ./...` (or `make test` if that is the canonical entry), full `./node_modules/.bin/vp test`, and the full affected Playwright e2e-full suite. All must pass; report any failure instead of pushing.

- [ ] **Step 2: Small-change verification**

Invoke the `small-change-verification` skill for the cross-layer metadata change checklist.

- [ ] **Step 3: Update PR #817 description**

Read `context/pull-request-workflow.md`, then rewrite the PR body: user-visible outcome (strict date order collapses exactly the commits no longer on the branch, for every force-push pattern), the pivot rationale (frontend replay cannot derive reachability; clone ancestry is ground truth), the skip semantics, and test coverage. Keep the existing title if `context/pull-request-workflow.md` allows (it still describes the user-visible outcome). Any PR *comment* posted must end with the `<sup>generated by a clanker</sup>` footer; the body itself follows the workflow doc's rules.

- [ ] **Step 4: Push**

Push to `origin t3code/fix-rewind-force-push-collapse` (existing PR branch; review-feedback pushes are allowed once validation has run).

---

## Self-Review Notes

- Spec coverage: sync stamping (Tasks 1–3), guard/skip semantics (Task 3 tests), frontend deletion + flag collapse (Task 4), component tests (Task 4), real-computation integration at the sync boundary plus DB→API→UI browser coverage (Tasks 3 and 5), data lifecycle needs no code (absent flag = no collapse falls out of `isObsoleteCommit`).
- The spec's provider-agnostic claim is honored via `syncProviderMRDiff` wiring (GitLab/Forgejo/Gitea share that diff path).
- Type consistency: `stampObsoleteCommitEvents(ctx, repo RepoRef, mrID int64, headSHA string) error` is the only cross-task Go surface; `withObsoleteMetadata(string, bool) (string, bool)`; `HasCommit`/`IsAncestor` signatures match Task 1's definitions at all Task 3 call sites.
