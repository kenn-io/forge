package main

import (
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"time"

	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

type archiveContextRepositories []platform.RepoRef

func (repos archiveContextRepositories) ConfiguredRepositories(context.Context) ([]platform.RepoRef, error) {
	return repos, nil
}

func seedArchiveContext(ctx context.Context, database *db.DB, registry *platform.Registry) (*archive.Service, error) {
	repos := archiveContextRepositories{}
	for _, name := range []string{"widgets", "tools"} {
		repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", name))
		if err != nil {
			return nil, err
		}
		if repo == nil {
			return nil, fmt.Errorf("missing archive fixture %s", name)
		}
		repos = append(repos, platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: name, RepoPath: "acme/" + name, PlatformID: repo.PlatformRepoID})
		if err := database.EnsureDiscoveryArchives(ctx, []int64{repo.ID}, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	if err := updateArchiveContext(ctx, database, true, ""); err != nil {
		return nil, err
	}
	return archive.NewService(database, registry, nil, repos, nil, nil)
}

// Populate the fixture's persisted archive state; snapshot responses still come
// from the production archive service and its normal database queries.
func updateArchiveContext(ctx context.Context, database *db.DB, complete bool, head string) error {
	now := time.Now().UTC()
	for _, name := range []string{"widgets", "tools"} {
		repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", name))
		if err != nil {
			return err
		}
		if repo == nil {
			return fmt.Errorf("missing archive fixture %s", name)
		}
		var completed any = now
		scan := "complete"
		if !complete {
			completed = nil
			scan = "pending"
		}
		if _, err := database.WriteDB().ExecContext(ctx, `UPDATE forge_archive_repos SET collection_mode='full', initial_started_at=?, initial_completed_at=?, maintenance_watermark=?, maintenance_succeeded_at=?, issues_coverage='supported', merge_requests_coverage='supported', comments_coverage='supported', reviews_coverage='supported', inline_comments_coverage='supported', updated_at=? WHERE repo_id=?`, now, completed, completed, completed, now, repo.ID); err != nil {
			return err
		}
		if _, err := database.WriteDB().ExecContext(ctx, `UPDATE forge_archive_repo_scans SET status=?,updated_at=? WHERE repo_id=?`, scan, now, repo.ID); err != nil {
			return err
		}
		if _, err := database.WriteDB().ExecContext(ctx, `UPDATE forge_repos SET last_sync_completed_at=? WHERE id=?`, now, repo.ID); err != nil {
			return err
		}
		if _, err := database.WriteDB().ExecContext(ctx, `UPDATE forge_merge_requests SET detail_fetched_at=? WHERE repo_id=?`, now, repo.ID); err != nil {
			return err
		}
		if head != "" {
			mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 1)
			if err != nil {
				return err
			}
			if mr == nil {
				return fmt.Errorf("missing archive PR fixture %s", name)
			}
			mr.PlatformHeadSHA = head
			mr.UpdatedAt = now
			if _, err := database.UpsertMergeRequest(ctx, mr); err != nil {
				return err
			}
		}
	}
	return nil
}

func archiveContextControl(database *db.DB, w http.ResponseWriter, r *http.Request) {
	var input struct {
		Complete bool   `json:"complete"`
		HeadSHA  string `json:"head_sha"`
	}
	if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 4096), &input, json.RejectUnknownMembers(true)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if input.HeadSHA != "" {
		head, err := hex.DecodeString(input.HeadSHA)
		if err != nil || len(head) != 20 {
			http.Error(w, "head_sha must be a full SHA", http.StatusBadRequest)
			return
		}
	}
	if err := updateArchiveContext(r.Context(), database, input.Complete, input.HeadSHA); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
