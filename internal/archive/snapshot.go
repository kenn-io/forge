package archive

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.kenn.io/forge/internal/archive/snapshot"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/platform"
)

type SnapshotOptions struct {
	Start, End   time.Time
	Repositories []platform.RepoRef
}

var ErrSnapshotScope = errors.New("snapshot repositories must belong to the configured cached inventory")

// Snapshot exports the configured cached inventory in one read transaction.
// It never starts sync, records hot views, or resolves identities with a provider.
func (s *Service) Snapshot(ctx context.Context, opts SnapshotOptions) (snapshot.ArchiveSnapshot, error) {
	return s.snapshot(ctx, opts, nil)
}

func (s *Service) snapshot(ctx context.Context, opts SnapshotOptions, afterCoverage func() error) (snapshot.ArchiveSnapshot, error) {
	start, end := opts.Start, opts.End
	result := snapshot.ArchiveSnapshot{ExportSchema: snapshot.Schema, ObservedAt: s.now().UTC(), Start: start.UTC(), End: end.UTC(), Repositories: []snapshot.SnapshotRepository{}, PullRequests: []snapshot.SnapshotPullRequest{}, Issues: []snapshot.SnapshotItem{}, Relations: []snapshot.SnapshotRelation{}}
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return result, errors.New("snapshot start must precede end")
	}
	refs, err := s.configuredRepositories(ctx)
	if err != nil {
		return result, err
	}
	if len(refs) == 0 {
		return result, ErrEmptyReportScope
	}
	tx, err := s.db.ReadDB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	selected := map[int64]bool{}
	for _, ref := range opts.Repositories {
		repo, err := db.LoadArchiveSnapshotRepository(ctx, tx, platformdb.DBRepoIdentity(ref))
		if err != nil {
			return result, err
		}
		if repo == nil {
			return result, ErrSnapshotScope
		}
		selected[repo.ID] = false
	}
	repos := map[int64]db.Repo{}
	repoIDs := []int64{}
	coverage, err := db.LoadArchiveReportRepositories(ctx, tx, nil, result.ObservedAt)
	if err != nil {
		return result, err
	}
	coverageByID := map[int64]db.ArchiveReportRepositoryRow{}
	for _, row := range coverage {
		coverageByID[row.RepoID] = row
	}
	for _, ref := range refs {
		repo, err := db.LoadArchiveSnapshotRepository(ctx, tx, platformdb.DBRepoIdentity(ref))
		if err != nil {
			return result, err
		}
		if repo == nil {
			if len(selected) > 0 {
				continue
			}
			result.Repositories = append(result.Repositories, snapshot.SnapshotRepository{ID: "unresolved:" + string(ref.Platform) + ":" + ref.Host + ":" + ref.RepoPath, Provider: string(ref.Platform), Host: ref.Host, ProviderID: ref.PlatformExternalID, Path: ref.RepoPath, SyncError: "Configured repository has no active cached identity"})
			continue
		}
		if len(selected) > 0 {
			if _, ok := selected[repo.ID]; !ok {
				continue
			}
			selected[repo.ID] = true
		}
		if _, exists := repos[repo.ID]; exists {
			continue
		}
		repos[repo.ID] = *repo
		repoIDs = append(repoIDs, repo.ID)
		record := snapshot.SnapshotRepository{ID: snapshotRepositoryID(*repo), Provider: repo.Platform, Host: repo.PlatformHost, ProviderID: repo.PlatformRepoID, Path: repo.RepoPath, DefaultBranch: repo.DefaultBranch, LastSyncAt: repo.LastSyncCompletedAt, SyncError: repo.LastSyncError}
		if row, ok := coverageByID[repo.ID]; ok {
			value := snapshot.SnapshotCoverage(reportCoverage(row))
			record.Coverage = &value
		}
		result.Repositories = append(result.Repositories, record)
	}
	for _, found := range selected {
		if !found {
			return result, ErrSnapshotScope
		}
	}
	if afterCoverage != nil {
		if err := afterCoverage(); err != nil {
			return result, err
		}
	}
	measurement, err := db.MeasureArchiveSnapshot(ctx, tx, repoIDs, start, end)
	if err != nil {
		return result, err
	}
	if measurement.Records > snapshot.MaxRecords || measurement.TextBytes > snapshot.MaxBytes {
		return snapshot.ArchiveSnapshot{}, snapshot.ErrTooLarge
	}
	items, err := db.LoadArchiveSnapshotItems(ctx, tx, repoIDs, start, end)
	if err != nil {
		return result, err
	}
	mrIDs := []int64{}
	pullPositions := map[int64]int{}
	issueIDs := map[int64]string{}
	for _, row := range items {
		repo := repos[row.RepoID]
		body, cut := snapshotText(row.Body, 8192)
		labels := []string{}
		if err := json.Unmarshal([]byte(row.LabelsJSON), &labels); err != nil {
			return result, fmt.Errorf("decode cached labels: %w", err)
		}
		slices.Sort(labels)
		item := snapshot.SnapshotItem{ID: snapshotRepositoryID(repo) + ":" + row.Kind + ":" + strconv.Itoa(row.Number), RepositoryID: snapshotRepositoryID(repo), Number: row.Number, URL: row.URL, Title: row.Title, Author: row.Author, AuthorAssociation: row.AuthorAssociation, Body: body, BodyTruncated: cut, State: row.State, Labels: labels, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(), DetailFetchedAt: row.DetailFetchedAt}
		if row.Kind == "issue" {
			issueIDs[row.ID] = item.ID
			result.Issues = append(result.Issues, item)
			continue
		}
		mrIDs = append(mrIDs, row.ID)
		pullPositions[row.ID] = len(result.PullRequests)
		pull := snapshot.SnapshotPullRequest{SnapshotItem: item, Draft: row.Draft, HeadSHA: row.HeadSHA, HeadBranch: row.HeadBranch, BaseBranch: row.BaseBranch, ChangedFiles: row.FilesChanged, ReviewState: row.ReviewDecision, CheckState: row.CIStatus, MergeableState: row.MergeableState, Checks: []snapshot.SnapshotCheck{}, Reviews: []snapshot.SnapshotReview{}, Gaps: []string{"readiness_observation_time_unknown"}}
		if !row.HeadRepoIdentityStale && repo.CloneURL != "" && row.HeadRepoCloneURL != "" {
			pull.HeadInSameRepository = new(repo.CloneURL == row.HeadRepoCloneURL)
		}
		// Older storage cannot distinguish an observed zero from an omitted size.
		// Export positive observations and leave ambiguous zero counts unknown.
		if row.Additions > 0 {
			pull.Additions = new(row.Additions)
		}
		if row.Deletions > 0 {
			pull.Deletions = new(row.Deletions)
		}
		if row.ChecksJSON != "" {
			var checks []db.CICheck
			if err := json.Unmarshal([]byte(row.ChecksJSON), &checks); err != nil {
				pull.Gaps = append(pull.Gaps, "invalid_cached_checks")
			} else {
				for _, check := range checks {
					pull.Checks = append(pull.Checks, snapshot.SnapshotCheck{Name: check.Name, Status: check.Status, Conclusion: check.Conclusion, URL: check.URL})
				}
			}
		}
		result.PullRequests = append(result.PullRequests, pull)
	}
	reviews, err := db.LoadArchiveSnapshotReviews(ctx, tx, mrIDs)
	if err != nil {
		return result, err
	}
	for _, row := range reviews {
		pull := &result.PullRequests[pullPositions[row.MergeRequestID]]
		body, cut := snapshotText(row.Body, 2048)
		pull.Reviews = append(pull.Reviews, snapshot.SnapshotReview{ID: pull.ID + "/review/" + url.QueryEscape(row.Key), Author: row.Author, AuthorAssociation: row.AuthorAssociation, Body: body, BodyTruncated: cut, State: row.State, URL: row.URL, CreatedAt: row.CreatedAt.UTC()})
	}
	links, err := db.LoadArchiveSnapshotReferences(ctx, tx, mrIDs)
	if err != nil {
		return result, err
	}
	for _, link := range links {
		pull := &result.PullRequests[pullPositions[link.MergeRequestID]]
		issueID, ok := issueIDs[link.IssueID]
		if !ok || !link.Resolved {
			if !slices.Contains(pull.Gaps, "unresolved_issue_reference") {
				pull.Gaps = append(pull.Gaps, "unresolved_issue_reference")
			}
			continue
		}
		result.Relations = append(result.Relations, snapshot.SnapshotRelation{SourceID: pull.ID, TargetID: issueID, Kind: "linked_issue", EvidenceID: issueID + "/reference/" + url.QueryEscape(link.EventKey), URL: link.URL, ObservedAt: link.ObservedAt.UTC()})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if len(encoded) > snapshot.MaxBytes {
		return snapshot.ArchiveSnapshot{}, snapshot.ErrTooLarge
	}
	if err := tx.Commit(); err != nil {
		return snapshot.ArchiveSnapshot{}, err
	}
	return result, nil
}

func snapshotRepositoryID(repo db.Repo) string {
	return strings.Join([]string{url.QueryEscape(repo.Platform), url.QueryEscape(repo.PlatformHost), url.QueryEscape(repo.PlatformRepoID)}, ":")
}

func snapshotText(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit], true
}
