package db

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKataIssueLinkCRUDUsesStableSubjectIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repoID, workspaceID := insertKataLinkSubjectsForTest(t, database)

	issueSubject := KataLinkSubject{
		Kind: KataLinkSubjectIssue, RepoID: repoID,
		ProviderItemExternalID: "provider-item-A",
	}
	pullSubject := KataLinkSubject{
		Kind: KataLinkSubjectPullRequest, RepoID: repoID,
		ProviderItemExternalID: "provider-item-A",
	}
	workspaceSubject := KataLinkSubject{
		Kind: KataLinkSubjectWorkspace, WorkspaceID: workspaceID,
	}

	issueLink, err := database.CreateKataIssueLink(t.Context(), KataIssueLink{
		Subject: issueSubject, DaemonID: "primary", ProjectUID: "project-a", IssueUID: "issue-a",
	})
	require.NoError(err)
	pullLink, err := database.CreateKataIssueLink(t.Context(), KataIssueLink{
		Subject: pullSubject, DaemonID: "primary", ProjectUID: "project-a", IssueUID: "issue-b",
	})
	require.NoError(err)
	workspaceLink, err := database.CreateKataIssueLink(t.Context(), KataIssueLink{
		Subject: workspaceSubject, DaemonID: "secondary", ProjectUID: "project-b", IssueUID: "issue-c",
	})
	require.NoError(err)

	assert.Positive(issueLink.ID)
	assert.Positive(pullLink.ID)
	assert.Positive(workspaceLink.ID)
	assert.Equal(issueSubject, issueLink.Subject)
	assert.Equal(time.UTC, issueLink.CreatedAt.Location())
	assert.Equal(time.UTC, issueLink.UpdatedAt.Location())
	assert.False(issueLink.CreatedAt.IsZero())
	assert.Equal(issueLink.CreatedAt, issueLink.UpdatedAt)

	issueLinks, err := database.ListKataIssueLinks(t.Context(), issueSubject)
	require.NoError(err)
	require.Len(issueLinks, 1)
	assert.Equal(issueLink, issueLinks[0])

	pullLinks, err := database.ListKataIssueLinks(t.Context(), pullSubject)
	require.NoError(err)
	require.Len(pullLinks, 1)
	assert.Equal(pullLink, pullLinks[0])

	workspaceLinks, err := database.ListKataIssueLinks(t.Context(), workspaceSubject)
	require.NoError(err)
	require.Len(workspaceLinks, 1)
	assert.Equal(workspaceLink, workspaceLinks[0])

	itemNumberLinks, err := database.ListKataIssueLinks(t.Context(), KataLinkSubject{
		Kind: KataLinkSubjectIssue, RepoID: repoID, ProviderItemExternalID: "42",
	})
	require.NoError(err)
	assert.Empty(itemNumberLinks, "provider item number must never substitute for stable external identity")

	deleted, err := database.DeleteKataIssueLink(t.Context(), pullSubject, issueLink.ID)
	require.NoError(err)
	assert.False(deleted, "a link ID must not be deletable through a different subject")

	deleted, err = database.DeleteKataIssueLink(t.Context(), issueSubject, issueLink.ID)
	require.NoError(err)
	assert.True(deleted)
	issueLinks, err = database.ListKataIssueLinks(t.Context(), issueSubject)
	require.NoError(err)
	assert.Empty(issueLinks)
}

func TestCreateKataIssueLinkIsIdempotentAndRefreshesProjectUID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repoID := insertTestRepo(t, database, "acme", "widget")
	subject := KataLinkSubject{
		Kind: KataLinkSubjectIssue, RepoID: repoID,
		ProviderItemExternalID: "provider-item-A",
	}
	input := KataIssueLink{
		Subject: subject, DaemonID: "primary", ProjectUID: "project-a", IssueUID: "issue-a",
	}

	first, err := database.CreateKataIssueLink(t.Context(), input)
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE kata_issue_links
		SET created_at = '2020-01-02T03:04:05Z',
		    updated_at = '2020-01-02T03:04:05Z'
		WHERE id = ?`, first.ID)
	require.NoError(err)

	input.ProjectUID = "project-b"
	refreshed, err := database.CreateKataIssueLink(t.Context(), input)
	require.NoError(err)
	assert.Equal(first.ID, refreshed.ID)
	assert.Equal("project-b", refreshed.ProjectUID)
	assert.Equal(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), refreshed.CreatedAt)
	assert.True(refreshed.UpdatedAt.After(refreshed.CreatedAt))

	const callers = 16
	start := make(chan struct{})
	results := make(chan KataIssueLink, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			link, createErr := database.CreateKataIssueLink(t.Context(), input)
			results <- link
			errs <- createErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for createErr := range errs {
		require.NoError(createErr)
	}
	for link := range results {
		assert.Equal(first.ID, link.ID)
		assert.Equal("project-b", link.ProjectUID)
	}
	var count int
	require.NoError(database.ReadDB().QueryRowContext(
		t.Context(), `SELECT COUNT(*) FROM kata_issue_links`,
	).Scan(&count))
	assert.Equal(1, count)
}

func TestKataIssueLinkValidationRejectsAmbiguousAndBlankIdentities(t *testing.T) {
	assert := assert.New(t)
	database := openTestDB(t)

	validProvider := KataLinkSubject{
		Kind: KataLinkSubjectIssue, RepoID: 1, ProviderItemExternalID: "provider-item-A",
	}
	validWorkspace := KataLinkSubject{
		Kind: KataLinkSubjectWorkspace, WorkspaceID: "workspace-a",
	}
	tests := []struct {
		name string
		link KataIssueLink
	}{
		{name: "unknown subject", link: KataIssueLink{Subject: KataLinkSubject{Kind: "unknown"}, DaemonID: "d", ProjectUID: "p", IssueUID: "i"}},
		{name: "provider without repo", link: KataIssueLink{Subject: KataLinkSubject{Kind: KataLinkSubjectIssue, ProviderItemExternalID: "provider-item-A"}, DaemonID: "d", ProjectUID: "p", IssueUID: "i"}},
		{name: "provider without external id", link: KataIssueLink{Subject: KataLinkSubject{Kind: KataLinkSubjectIssue, RepoID: 1}, DaemonID: "d", ProjectUID: "p", IssueUID: "i"}},
		{name: "provider with workspace", link: KataIssueLink{Subject: KataLinkSubject{Kind: KataLinkSubjectIssue, RepoID: 1, ProviderItemExternalID: "provider-item-A", WorkspaceID: "workspace-a"}, DaemonID: "d", ProjectUID: "p", IssueUID: "i"}},
		{name: "workspace without id", link: KataIssueLink{Subject: KataLinkSubject{Kind: KataLinkSubjectWorkspace}, DaemonID: "d", ProjectUID: "p", IssueUID: "i"}},
		{name: "workspace with repo", link: KataIssueLink{Subject: KataLinkSubject{Kind: KataLinkSubjectWorkspace, RepoID: 1, WorkspaceID: "workspace-a"}, DaemonID: "d", ProjectUID: "p", IssueUID: "i"}},
		{name: "workspace with provider id", link: KataIssueLink{Subject: KataLinkSubject{Kind: KataLinkSubjectWorkspace, ProviderItemExternalID: "provider-item-A", WorkspaceID: "workspace-a"}, DaemonID: "d", ProjectUID: "p", IssueUID: "i"}},
		{name: "blank provider external id", link: KataIssueLink{Subject: KataLinkSubject{Kind: KataLinkSubjectIssue, RepoID: 1, ProviderItemExternalID: "  "}, DaemonID: "d", ProjectUID: "p", IssueUID: "i"}},
		{name: "blank workspace id", link: KataIssueLink{Subject: KataLinkSubject{Kind: KataLinkSubjectWorkspace, WorkspaceID: "  "}, DaemonID: "d", ProjectUID: "p", IssueUID: "i"}},
		{name: "blank daemon id", link: KataIssueLink{Subject: validProvider, DaemonID: "  ", ProjectUID: "p", IssueUID: "i"}},
		{name: "blank project uid", link: KataIssueLink{Subject: validProvider, DaemonID: "d", ProjectUID: "  ", IssueUID: "i"}},
		{name: "blank issue uid", link: KataIssueLink{Subject: validProvider, DaemonID: "d", ProjectUID: "p", IssueUID: "  "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := database.CreateKataIssueLink(t.Context(), tt.link)
			require.Error(t, err)
		})
	}

	_, err := database.ListKataIssueLinks(t.Context(), KataLinkSubject{
		Kind: KataLinkSubjectIssue, RepoID: 1, ProviderItemExternalID: " ",
	})
	require.Error(t, err)
	_, err = database.DeleteKataIssueLink(t.Context(), validWorkspace, 1)
	require.NoError(t, err)
	assert.Zero(kataIssueLinkCountForTest(t, database))
}

func TestKataIssueLinksSurviveRepoRenameAndCascadeWithOwners(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repoID, workspaceID := insertKataLinkSubjectsForTest(t, database)
	repoSubject := KataLinkSubject{
		Kind: KataLinkSubjectIssue, RepoID: repoID,
		ProviderItemExternalID: "provider-item-A",
	}
	workspaceSubject := KataLinkSubject{
		Kind: KataLinkSubjectWorkspace, WorkspaceID: workspaceID,
	}
	_, err := database.CreateKataIssueLink(t.Context(), KataIssueLink{
		Subject: repoSubject, DaemonID: "primary", ProjectUID: "project-a", IssueUID: "issue-a",
	})
	require.NoError(err)
	_, err = database.CreateKataIssueLink(t.Context(), KataIssueLink{
		Subject: workspaceSubject, DaemonID: "primary", ProjectUID: "project-a", IssueUID: "issue-b",
	})
	require.NoError(err)

	identity := verifiedTestRepoIdentity("github", "github.com", "acme", "widget")
	identity.Owner = "renamed"
	identity.Name = "renamed-widget"
	entry, accepted, err := database.ReconcileRepositoryObservation(
		t.Context(), identity, time.Now().UTC().Add(time.Hour),
	)
	require.NoError(err)
	assert.True(accepted)
	require.NotNil(entry)
	assert.Equal(repoID, entry.Repository.ID)
	repoLinks, err := database.ListKataIssueLinks(t.Context(), repoSubject)
	require.NoError(err)
	assert.Len(repoLinks, 1)

	_, err = database.WriteDB().ExecContext(t.Context(), `DELETE FROM forge_repos WHERE id = ?`, repoID)
	require.Error(err, "a workspace keeps its stable repository identity alive")
	assert.Equal(2, kataIssueLinkCountForTest(t, database))
	_, err = database.WriteDB().ExecContext(t.Context(), `DELETE FROM forge_workspaces WHERE id = ?`, workspaceID)
	require.NoError(err)
	assert.Equal(1, kataIssueLinkCountForTest(t, database))
	_, err = database.WriteDB().ExecContext(t.Context(), `DELETE FROM forge_repos WHERE id = ?`, repoID)
	require.NoError(err)
	assert.Zero(kataIssueLinkCountForTest(t, database))
	assertDatabaseIntegrityForTest(t, database.ReadDB())
}

func insertKataLinkSubjectsForTest(t *testing.T, database *DB) (int64, string) {
	t.Helper()
	repoID := insertTestRepo(t, database, "acme", "widget")
	issue := testIssue(repoID, 42)
	issue.PlatformExternalID = "provider-item-A"
	_, err := database.UpsertIssue(t.Context(), issue)
	require.NoError(t, err)
	pull := testMR(repoID, 42)
	pull.PlatformExternalID = "provider-item-A"
	_, err = database.UpsertMergeRequest(t.Context(), pull)
	require.NoError(t, err)

	const workspaceID = "workspace-a"
	require.NoError(t, database.InsertWorkspace(t.Context(), &Workspace{
		ID: workspaceID, Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: WorkspaceItemTypePullRequest,
		ItemNumber: 42, GitHeadRef: "feature", WorkspaceBranch: "feature",
		WorktreePath: "/tmp/workspace-a", TmuxSession: "workspace-a", Status: "ready",
	}))
	return repoID, workspaceID
}

func kataIssueLinkCountForTest(t *testing.T, database *DB) int {
	t.Helper()
	var count int
	require.NoError(t, database.ReadDB().QueryRowContext(
		t.Context(), `SELECT COUNT(*) FROM kata_issue_links`,
	).Scan(&count))
	return count
}
