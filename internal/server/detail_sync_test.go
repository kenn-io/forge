package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/testutil/tokenauthtest"
	platformgithub "go.kenn.io/forge/platform/github"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestEnqueueDetailSyncOrRerunRunsPendingAfterInFlight(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, _, _ := setupTestServer(t)

	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		secondDone := make(chan struct{})
		var calls atomic.Int64

		started := srv.syncevents.EnqueueDetailSyncOrRerun("pr:github:github.com:acme/widget#7", nil, db.Repo{}, 0, func(context.Context) error {
			calls.Add(1)
			close(firstStarted)
			<-releaseFirst
			return nil
		})
		require.True(started)
		synctest.Wait()
		<-firstStarted

		started = srv.syncevents.EnqueueDetailSyncOrRerun("pr:github:github.com:acme/widget#7", nil, db.Repo{}, 0, func(context.Context) error {
			calls.Add(1)
			close(secondDone)
			return nil
		})
		assert.False(started, "duplicate detail sync should report deduped while queuing a rerun")

		close(releaseFirst)
		synctest.Wait()
		<-secondDone
		assert.Equal(int64(2), calls.Load())
		srv.bg.Wait()
	})
}

func TestEnqueueDetailSyncOrRerunKeepsReplacedCompletion(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, _, _ := setupTestServer(t)

	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		const key = "pr:github:github.com:acme/widget#7"
		releaseFirst := make(chan struct{})
		var order []string
		record := func(step string) func(context.Context) error {
			return func(context.Context) error {
				order = append(order, step)
				return nil
			}
		}

		require.True(srv.syncevents.EnqueueDetailSyncOrRerun(key, nil, db.Repo{}, 0, func(context.Context) error {
			<-releaseFirst
			return nil
		}))
		synctest.Wait()

		// A mutation queues a rerun with follow-up work, then a plain refresh
		// replaces that rerun before the in-flight sync finishes.
		require.False(srv.syncevents.EnqueueDetailSyncOrRerunWithCompletion(
			key, nil, db.Repo{}, 0, record("mutation sync"),
			func(context.Context) { order = append(order, "mutation completion") },
		))
		require.False(srv.syncevents.EnqueueDetailSyncOrRerun(key, nil, db.Repo{}, 0, record("refresh sync")))

		close(releaseFirst)
		synctest.Wait()
		srv.bg.Wait()
		assert.Equal([]string{"refresh sync", "mutation completion"}, order)
	})
}

func TestEnqueuePRSyncBroadcastsSnapshotChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed bool
	}{
		{"unchanged", false},
		{"queue", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverfake.RunParallelServerTest(t)
			when := gh.Timestamp{Time: time.Now().UTC().Add(time.Minute).Truncate(time.Second)}
			comment, state := "original", "open"
			commentWhen := gh.Timestamp{Time: when.Add(time.Minute)}
			mock := &serverfake.MockGH{
				GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
					return &gh.PullRequest{ID: new(int64(1000)), Number: &number, State: &state, Title: new("Example pull"), Body: new("body"), CreatedAt: &when, UpdatedAt: &when, Head: &gh.PullRequestBranch{SHA: new("abc123"), Ref: new("feature")}, Base: &gh.PullRequestBranch{Ref: new("main")}}, nil
				},
				ListPRTimelineEventsFn: func(context.Context, string, string, int) ([]platformgithub.PullRequestTimelineEvent, error) {
					return []platformgithub.PullRequestTimelineEvent{{NodeID: "title-1", EventType: "renamed_title", PreviousTitle: "Old", CurrentTitle: "New", CreatedAt: when.Time}}, nil
				},
				ListIssueCommentsFn: func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
					return []*gh.IssueComment{{ID: new(int64(99)), Body: &comment, CreatedAt: &commentWhen, UpdatedAt: &commentWhen}}, nil
				},
			}
			srv, database, syncer := setupTestServerWithMock(t, mock)
			serverfake.SeedPR(t, database, "acme", "widget", 1)
			input := &itemapi.RepoNumberInput{Provider: "github", Owner: "acme", Name: "widget", Number: 1}
			_, err := srv.activityapi.EnqueuePRSync(t.Context(), input)
			require.NoError(t, err)
			srv.bg.Wait()
			result, err := database.WriteDB().ExecContext(t.Context(), `UPDATE forge_mr_events SET metadata_json = '{"current_title":"New","previous_title":"Old"}' WHERE dedupe_key = 'timeline-title-1'`)
			require.NoError(t, err)
			rows, err := result.RowsAffected()
			require.NoError(t, err)
			require.EqualValues(t, 1, rows)

			if tc.changed {
				queueServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Connection", "close")
					switch {
					case strings.Contains(string(body), "isMergeQueueEnabled"):
						_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{"state":"OPEN","baseRefName":"main","isMergeQueueEnabled":true,"mergeQueueEntry":{"position":1,"state":"AWAITING_CHECKS","enqueuedAt":"2026-10-14T19:05:15Z","estimatedTimeToMerge":240},"latestQueueEvent":{"nodes":[{"__typename":"AddedToMergeQueueEvent"}]}}}}}`)
					case strings.Contains(string(body), "configuration"):
						_, _ = io.WriteString(w, `{"data":{"repository":{"mergeQueue":{"configuration":{"mergeMethod":"SQUASH"}}}}}`)
					default:
						_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{}}}}`)
					}
				}))
				t.Cleanup(queueServer.Close)
				syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{"github.com": ghclient.NewGraphQLFetcher(tokenauthtest.Source("token"), "github.com", nil, nil, ghclient.WithGraphQLEndpointForTesting(queueServer.URL))})
			}
			synctest.Test(t, func(t *testing.T) {
				oldHub := srv.hub
				srv.hub = syncevents.NewEventHub()
				oldHub.Close()
				defer srv.hub.Close()
				events, _ := srv.hub.Subscribe(t.Context(), false)
				_, err = srv.activityapi.EnqueuePRSync(t.Context(), input)
				require.NoError(t, err)
				srv.bg.Wait()
				time.Sleep(syncevents.DataChangedInterval)
				synctest.Wait()
				var change syncevents.Event
			drain:
				for {
					select {
					case event := <-events:
						if event.Event.Type == "data_changed" {
							require.Empty(t, change.Type, "extra data_changed event")
							change = event.Event
						}
					default:
						break drain
					}
				}
				if change.Type != "" {
					require.True(t, tc.changed, "unchanged refresh sent an event")
					payload, err := json.Marshal(change.Data)
					require.NoError(t, err)
					var data struct {
						Repositories []struct {
							Provider     string `json:"provider"`
							PlatformHost string `json:"platform_host"`
							RepoPath     string `json:"repo_path"`
						} `json:"repositories"`
					}
					require.NoError(t, json.Unmarshal(payload, &data))
					require.Len(t, data.Repositories, 1)
					require.Equal(t, "github", data.Repositories[0].Provider)
					require.Equal(t, "github.com", data.Repositories[0].PlatformHost)
					require.Equal(t, "acme/widget", data.Repositories[0].RepoPath)
				} else {
					require.False(t, tc.changed, "changed refresh did not broadcast")
				}
			})
		})
	}
}
