# Gitea Inline Review and Gitealike Archive Coverage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sync Gitea inline review comments through canonical merge-request detail sync and report inline archive coverage for validated Gitea and Forgejo versions.

**Architecture:** Add a Gitea-specific `MergeRequestReviewThreadReader` parallel to Forgejo's existing reader, gated at client construction by the validated Gitea version floor. Keep archive hydration unchanged: it invokes ordinary item sync, which commits the complete review-thread dataset through the existing revision-fenced writer.

**Tech Stack:** Go 1.25, Gitea SDK, Forgejo SDK, SQLite, testify, Python container fixture bootstrap, testcontainers-go.

## Global Constraints

- Advertise Gitea review-thread reads and archive inline-comment coverage only for Gitea 1.24.6 or newer.
- Keep Forgejo on its existing canonical review-thread reader and declare its archive inline-comment coverage only after live validation.
- Do not add an archive-specific provider read, normalization, persistence, or compatibility path.
- A reader error must reject the full dataset; never commit a successful partial result.
- Preserve full provider/host/repository identity plus provider review, thread, and comment IDs and all provider-supplied review metadata.
- Invoke direct Go tests with `-shuffle=on`, omit `-count=1`, and use testify assertions.

---

### Task 1: Add the version-gated Gitea review-thread reader

**Files:**
- Modify: `internal/platform/gitea/client.go`
- Modify: `internal/platform/gitea/client_test.go`
- Create: `internal/platform/gitea/diff_review.go`
- Create: `internal/platform/gitea/diff_review_test.go`

**Interfaces:**
- Consumes: `giteasdk.Client.ListPullReviews`, `giteasdk.Client.ListPullReviewComments`, `platform.MergeRequestReviewThread`, and the existing `transport.withRequestContext` and `giteaHTTPError` helpers.
- Produces: `func (*Client) ListMergeRequestReviewThreads(context.Context, platform.RepoRef, int) ([]platform.MergeRequestReviewThread, error)` and version-aware `Client.Capabilities()`.

- [ ] **Step 1: Write failing capability and interface tests**

Add the interface assertion and a table-driven version-boundary test:

```go
_ platform.MergeRequestReviewThreadReader = (*Client)(nil)

func TestClientReviewThreadCapabilitiesUseValidatedVersionFloor(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		supported bool
	}{
		{name: "below floor", version: "1.24.5", supported: false},
		{name: "at floor", version: "1.24.6", supported: true},
		{name: "newer", version: "1.26.0", supported: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.NotFoundHandler())
			defer server.Close()
			client, err := NewClient(
				"gitea.test", testTokenSource("token"),
				WithBaseURLForTesting(server.URL),
				func(opts *clientOptions) { opts.serverVersion = tt.version },
			)
			require.NoError(t, err)
			caps := client.Capabilities()
			assert.Equal(t, tt.supported, caps.ReadReviewThreads)
			assert.Equal(t, tt.supported, caps.Archive.InlineReviewComments)
			if !tt.supported {
				_, err = client.ListMergeRequestReviewThreads(
					t.Context(), platform.RepoRef{Owner: "owner", Name: "repo"}, 42,
				)
				assert.ErrorIs(t, err, platform.ErrUnsupportedCapability)
			}
		})
	}
}
```

Also update `TestClientProviderIdentityExposesReadCapabilities` so the default test client expects `ReadReviewThreads` and archive inline-comment coverage to be true.

- [ ] **Step 2: Run the capability test and verify it fails**

Run:

```bash
go test ./internal/platform/gitea -run '^TestClientReviewThreadCapabilitiesUseValidatedVersionFloor$' -shuffle=on
```

Expected: compilation fails because `clientOptions.serverVersion`, `Client.Capabilities`, and `Client.ListMergeRequestReviewThreads` are not implemented.

- [ ] **Step 3: Write failing pagination, normalization, and atomic-failure tests**

Create `diff_review_test.go` with an `httptest.Server` that:

- returns page 1 of `/pulls/42/reviews` with a `Link: <...?page=2>; rel="next"` header;
- returns page 2 with a second review;
- returns two comments for the first review and one resolved left-side comment for the second;
- asserts the reader requests `limit=100` and pages 1 and 2;
- asserts all three normalized records retain review/comment IDs, author, body, URL, timestamps, path, side, old/new line, commit SHA, and resolution state.

Add a second test whose later review-comment endpoint returns HTTP 502 and assert:

```go
threads, err := client.ListMergeRequestReviewThreads(t.Context(), ref, 42)
require.Error(t, err)
assert.Nil(t, threads)
```

- [ ] **Step 4: Run the reader tests and verify they fail**

Run:

```bash
go test ./internal/platform/gitea -run 'TestListMergeRequestReviewThreads' -shuffle=on
```

Expected: compilation fails because the Gitea review-thread reader does not exist.

- [ ] **Step 5: Implement the version boundary and capability declaration**

Replace `skipVersionProbe` with an explicit optional server version and record whether the SDK constraint passes:

```go
const minimumReviewThreadVersion = ">= 1.24.6"

type clientOptions struct {
	baseURL           string
	foregroundTimeout time.Duration
	rateTracker       *ratelimit.RateTracker
	budget            *ghsync.SyncBudget
	serverVersion     string
}

type Client struct {
	host              string
	baseURL           string
	transport         *transport
	*provider
	api               *giteasdk.Client
	foregroundTimeout time.Duration
	readReviewThreads bool
}

func WithBaseURLForTesting(baseURL string) ClientOption {
	return func(opts *clientOptions) {
		opts.baseURL = strings.TrimRight(baseURL, "/")
		opts.serverVersion = "1.26.0"
	}
}
```

Append `giteasdk.SetGiteaVersion(opts.serverVersion)` only when the value is non-empty. After constructing the SDK client, compute:

```go
readReviewThreads := api.CheckServerVersionConstraint(minimumReviewThreadVersion) == nil
```

Store that value on `Client` and add:

```go
func (c *Client) Capabilities() platform.Capabilities {
	caps := c.provider.Capabilities()
	if c.readReviewThreads {
		caps.ReadReviewThreads = true
		caps.Archive.InlineReviewComments = true
	}
	return caps
}
```

- [ ] **Step 6: Implement the complete Gitea reader and normalizer**

Create `diff_review.go` with these functions:

```go
func (c *Client) ListMergeRequestReviewThreads(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestReviewThread, error) {
	if !c.readReviewThreads {
		return nil, platform.UnsupportedCapability(platform.KindGitea, c.host, "read_review_threads")
	}
	return c.transport.listMergeRequestReviewThreads(ctx, ref, number)
}

func (t *transport) listMergeRequestReviewThreads(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
) ([]platform.MergeRequestReviewThread, error) {
	reviews, err := t.listAllPullReviews(ctx, ref, number)
	if err != nil {
		return nil, err
	}
	threads := make([]platform.MergeRequestReviewThread, 0)
	for _, review := range reviews {
		comments, err := t.listPullReviewComments(ctx, ref, number, review.ID)
		if err != nil {
			return nil, err
		}
		for _, comment := range comments {
			threads = append(threads, giteaReviewThread(review, comment))
		}
	}
	return threads, nil
}
```

`listAllPullReviews` must request page size 100 and follow `resp.NextPage` until zero. `listPullReviewComments` must return every comment from the SDK endpoint. `giteaReviewThread` must map the same platform fields as Forgejo's proven normalizer while using Gitea SDK types and `convertUser`.

- [ ] **Step 7: Run and format the Gitea provider package**

Run:

```bash
gofmt -w internal/platform/gitea/client.go internal/platform/gitea/client_test.go internal/platform/gitea/diff_review.go internal/platform/gitea/diff_review_test.go
go test ./internal/platform/gitea -shuffle=on
```

Expected: package passes, including version-floor, pagination, normalization, and failure tests.

- [ ] **Step 8: Commit the Gitea provider reader**

Before committing, run the repository-local `context-sync` skill with `--commit`, then the mandatory commit skill. Stage only the four Gitea files and create a commit with subject:

```text
feat: sync inline Gitea review comments
```

The body must explain that support is gated at the container-validated 1.24.6 floor and that complete datasets flow through canonical MR detail sync.

### Task 2: Correct Forgejo archive coverage

**Files:**
- Modify: `internal/platform/forgejo/client.go`
- Modify: `internal/platform/forgejo/client_test.go`

**Interfaces:**
- Consumes: Forgejo's existing `Client.ListMergeRequestReviewThreads` implementation.
- Produces: `Archive.InlineReviewComments = true` whenever Forgejo already advertises `ReadReviewThreads = true`.

- [ ] **Step 1: Update the expected Forgejo capability test first**

Add inline archive coverage to `TestClientProviderIdentityExposesReadCapabilities`:

```go
Archive: platform.ArchiveCapabilities{
	HistoricalIssues: true, HistoricalMergeRequests: true,
	OrdinaryComments: true, SubmittedReviews: true,
	InlineReviewComments: true,
},
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
go test ./internal/platform/forgejo -run '^TestClientProviderIdentityExposesReadCapabilities$' -shuffle=on
```

Expected: FAIL because actual archive inline-comment support remains false.

- [ ] **Step 3: Tie Forgejo archive coverage to its existing reader capability**

Extend `Client.Capabilities`:

```go
func (c *Client) Capabilities() platform.Capabilities {
	caps := c.provider.Capabilities()
	caps.ReviewDraftMutation = true
	caps.ReadReviewThreads = true
	caps.Archive.InlineReviewComments = true
	caps.NativeMultilineRanges = false
	return caps
}
```

- [ ] **Step 4: Run the Forgejo provider tests**

Run:

```bash
gofmt -w internal/platform/forgejo/client.go internal/platform/forgejo/client_test.go
go test ./internal/platform/forgejo -shuffle=on
```

Expected: package passes.

- [ ] **Step 5: Commit the Forgejo coverage correction**

Run context-sync `--commit` and the mandatory commit skill, stage the two Forgejo files, and commit with subject:

```text
fix: report Forgejo inline archive coverage
```

The body must state that archive hydration already used the canonical reader and only the coverage declaration was false.

### Task 3: Prove regular sync and archive reuse against real Gitealike APIs

**Files:**
- Modify: `scripts/e2e/gitealike/bootstrap.py`
- Modify: `internal/server/gitealike_container_e2e_test.go`

**Interfaces:**
- Consumes: provider review creation endpoints, `ghclient.Syncer.SyncMROnProvider`, `archive.NewService`, `archive.Service.RunEligible`, `db.DB.ListMRReviewThreads`, and `archive.Service.Report`.
- Produces: fixture manifest fields `review_id`, `review_comment_id`, `review_comment_body`, `review_comment_author`, `review_comment_path`, and `review_comment_commit_sha`.

- [ ] **Step 1: Extend the fixture manifest contract in the Go test first**

Add these fields to `giteaLikeContainerManifest`:

```go
ReviewID               int64  `json:"review_id"`
ReviewCommentID        int64  `json:"review_comment_id"`
ReviewCommentBody      string `json:"review_comment_body"`
ReviewCommentAuthor    string `json:"review_comment_author"`
ReviewCommentPath      string `json:"review_comment_path"`
ReviewCommentCommitSHA string `json:"review_comment_commit_sha"`
```

Extend `giteaLikeContainerClient` with `platform.MergeRequestReviewThreadReader`. In `assertGiteaLikeContainerSync`, assert regular sync produces exactly the seeded thread and all manifest fields match the persisted row.

- [ ] **Step 2: Seed a real inline review comment in the shared fixture**

After creating the pull request, call a new idempotent `ensure_inline_review` method. It must list existing reviews and their comments, reuse the fixture comment when its body matches, and otherwise issue:

```python
review = self.request(
    "POST",
    self.repo_path(f"/pulls/{pull['number']}/reviews"),
    token=token,
    data={
        "event": "COMMENT",
        "body": f"Inline review from {self.title_prefix} container",
        "commit_id": commit_sha,
        "comments": [{
            "path": "src/gitealike-fixture.txt",
            "body": f"Inline note from {self.title_prefix} container",
            "new_position": 1,
        }],
    },
    expected=(200, 201),
)
```

Fetch `/pulls/{number}/reviews/{review_id}/comments`, select the matching inline body, and add its review/comment IDs, author, path, and commit SHA to the manifest.

- [ ] **Step 3: Add archive hydration and report assertions**

After proving regular sync, delete the synchronized review-thread dataset with `DeleteMissingMRReviewThreads` so archive hydration must restore it. Construct the real archive service over the same syncer:

```go
archiveService, err := archive.NewService(database, registry, syncer, syncer, nil, nil)
require.NoError(err)
ref := platform.RepoRef{
	Platform: kind, Host: manifest.Host, Owner: manifest.Owner,
	Name: manifest.Name, RepoPath: manifest.RepoPath,
}
require.NoError(archiveService.EnsureConfigured(ctx, []platform.RepoRef{ref}))
_, err = archiveService.Start(ctx, []platform.RepoRef{ref})
require.NoError(err)
```

Call `RunEligible` with a bounded iteration count until `Status` reports `db.ArchiveStatusCurrent`; fail if it never reaches current. Then assert:

```go
assert.Equal(db.ArchiveCoverageSupported, status[0].State.InlineCommentsCoverage)
threads, err = database.ListMRReviewThreads(ctx, mr.ID)
require.NoError(err)
require.Len(threads, 1)
assert.Equal(strconv.FormatInt(manifest.ReviewCommentID, 10), threads[0].ProviderCommentID)
```

Generate a detailed report spanning the fixture timestamp and assert repository coverage is `supported`, total inline review comments equal one, and the inline activity row carries the seeded provider comment ID, author, body, and URL.

- [ ] **Step 4: Run the non-container server package first**

Run:

```bash
gofmt -w internal/server/gitealike_container_e2e_test.go
go test ./internal/server -run 'Test(Forgejo|Gitea)ContainerSync' -shuffle=on
```

Expected: PASS with both tests skipped because their opt-in environment variables are absent; the package compiles with the expanded fixture contract.

- [ ] **Step 5: Run the real Gitea 1.24.6 container validation**

Run:

```bash
MIDDLEMAN_GITEA_CONTAINER_TESTS=1 go test ./internal/server -run '^TestGiteaContainerSync$' -shuffle=on -timeout 15m
```

Expected: PASS against the default `gitea/gitea:1.24.6` image, proving regular sync, archive hydration, persisted review metadata, supported coverage, and report counts.

- [ ] **Step 6: Run the real Forgejo container validation**

Run:

```bash
MIDDLEMAN_FORGEJO_CONTAINER_TESTS=1 go test ./internal/server -run '^TestForgejoContainerSync$' -shuffle=on -timeout 15m
```

Expected: PASS against the default Forgejo image with the same canonical-sync and archive assertions.

- [ ] **Step 7: Commit the live compatibility coverage**

Run context-sync `--commit` and the mandatory commit skill, stage the shared bootstrap and container test, and commit with subject:

```text
test: prove Gitealike inline archive hydration
```

The body must explain that the live fixtures guard API shape and prove archive hydration reuses ordinary merge-request sync.

### Task 4: Record the durable boundary and verify the whole change

**Files:**
- Modify: `context/platform-sync-invariants.md`

**Interfaces:**
- Consumes: the implemented Gitea and Forgejo capability methods and container validation.
- Produces: durable provider-sync guidance for future changes.

- [ ] **Step 1: Add the concise platform invariant**

Under `Forgejo And Gitea Shape`, add one anchored statement:

```markdown
Inline review ingestion stays in canonical merge-request detail sync. Forgejo
advertises its proven reader directly; Gitea advertises review threads and archive
inline-comment coverage only from the container-validated 1.24.6 floor.
(`internal/platform/forgejo/client.go::Capabilities`, `internal/platform/gitea/client.go::Capabilities`)
```

- [ ] **Step 2: Run focused and repository verification**

Run:

```bash
go test ./internal/platform/gitea ./internal/platform/forgejo ./internal/platform/gitealike ./internal/archive ./internal/github ./internal/server -shuffle=on
make test-short
make vet
```

Expected: all commands pass. The container commands from Task 3 remain the novel external-system validation evidence.

- [ ] **Step 3: Run completion verification**

Invoke `superpowers:verification-before-completion`, inspect `git status --short` and `git diff HEAD`, and rerun any command affected by final edits. Do not claim success from earlier output if code changed afterward.

- [ ] **Step 4: Commit the invariant and any final cohesive fixes**

Run `context-sync --commit`, then the mandatory commit skill. Commit the context update and any final directly related corrections with subject:

```text
docs: preserve canonical Gitealike archive sync
```

- [ ] **Step 5: Close Kata 801g with typed evidence**

After every required test and both container validations pass, close the issue with the final implementation commit:

```bash
final_implementation_sha=$(git rev-parse HEAD)
kata close 801g --done \
  --message "Gitea inline review comments now sync through canonical MR detail hydration at the validated 1.24.6 floor; Forgejo and Gitea container tests prove persisted rows, supported archive coverage, and report counts." \
  --commit "$final_implementation_sha" \
  --agent
```

Expected: Kata reports issue `801g` closed with commit evidence.
