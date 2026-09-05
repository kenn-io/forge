package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/archive/report"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
)

type archiveLandingInput struct {
	Repository string `query:"repo" required:"true" doc:"One canonical provider|platform_host/repo_path selector."`
}
type archiveLandingResponse struct {
	SnapshotSchema string                       `json:"schema"`
	Repository     platform.LandingRepository   `json:"repository"`
	Capabilities   platform.LandingCapabilities `json:"capabilities"`
	Coverage       platform.LandingCoverage     `json:"coverage"`
	Candidates     []platform.LandingCandidate  `json:"candidates"`
	StartedAt      time.Time                    `json:"started_at"`
	CompletedAt    time.Time                    `json:"completed_at"`
}

func (*archiveLandingResponse) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	if schema != nil && schema.Properties["schema"] != nil {
		schema.Properties["schema"].Extensions = map[string]any{"x-go-name": "SnapshotSchema"}
	}
	return schema
}

type archiveLandingOutput = httpapi.BodyOutput[archiveLandingResponse]

func (s *Server) registerArchiveLandingAPI(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "get-archive-landing-evidence", Method: http.MethodGet, Path: "/archive/landing-evidence", Summary: "Collect bounded landed-work provider evidence", Tags: []string{"Archive"}}, s.getArchiveLandingEvidence)
}

func (s *Server) getArchiveLandingEvidence(ctx context.Context, input *archiveLandingInput) (*archiveLandingOutput, error) {
	if s.syncer == nil {
		return nil, httpapi.ServiceUnavailable("provider collection is not configured")
	}
	refs, err := archiveQueryRefs([]string{input.Repository})
	if err != nil {
		return nil, err
	}
	if len(refs) != 1 {
		return nil, httpapi.Validation("query.repo", "exactly one repository is required")
	}
	// Match the archive command's existing operation timeout and report limits.
	// No SQLite read transaction or reconciliation lease spans provider I/O.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ref := refs[0]
	identity := platformdb.DBRepoIdentity(ref)
	before, err := s.db.GetRepositoryProviderSnapshot(ctx, identity)
	if err != nil {
		return nil, httpapi.Internal("resolve repository identity")
	}
	if before == nil || before.Repository.PlatformRepoID == "" {
		return nil, httpapi.NotFound(httpapi.CodeRepoNotFound, "repository has no verified provider identity", nil)
	}
	ref.PlatformExternalID = before.Repository.PlatformRepoID
	// The existing GitHub catalog stores its opaque node ID. The provider
	// verifies that observation alongside REST identity; it is not substituted
	// for the numeric identity carried by the new evidence contract.
	if ref.Platform != platform.KindGitHub {
		ref.PlatformID, err = strconv.ParseInt(before.Repository.PlatformRepoID, 10, 64)
		if err != nil || ref.PlatformID <= 0 {
			return nil, httpapi.Conflict(httpapi.CodeConflict, "repository identity is not usable for evidence collection", nil)
		}
	}
	budget := platform.Budget{MaxRecords: report.MaxDetailedRecords, MaxNodes: report.MaxDetailedRecords, MaxBytes: report.MaxDetailedTextBytes, MaxOutputBytes: report.MaxDetailedTextBytes}
	snapshot, err := s.syncer.CollectLandingEvidence(ctx, ref, budget)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, platform.ErrPageLimit) {
			return nil, httpapi.NewProblem(http.StatusRequestEntityTooLarge, httpapi.CodePayloadTooLarge, "landing evidence exceeds the report envelope", nil)
		}
		return nil, httpapi.ProviderCallProblem(err, string(ref.Platform), ref.Host)
	}
	after, err := s.db.GetRepositoryProviderSnapshot(ctx, identity)
	if err != nil {
		return nil, httpapi.Internal("recheck repository identity")
	}
	if after == nil || before.Repository.ID != after.Repository.ID || before.Repository.PlatformRepoID != after.Repository.PlatformRepoID || before.Route.ID != after.Route.ID || before.Route.Generation != after.Route.Generation {
		return nil, httpapi.Conflict(httpapi.CodeConflict, "repository changed during evidence collection", nil)
	}
	if snapshot.Schema != platform.LandingSnapshotSchema || snapshot.Repository.Identity.Provider != ref.Platform || snapshot.Repository.Identity.Instance != ref.Host || snapshot.Repository.Identity.ID == "" {
		return nil, httpapi.Internal("provider returned inconsistent evidence identity")
	}
	return &archiveLandingOutput{Body: archiveLandingResponse{
		SnapshotSchema: snapshot.Schema, Repository: snapshot.Repository,
		Capabilities: snapshot.Capabilities, Coverage: snapshot.Coverage,
		Candidates: snapshot.Candidates, StartedAt: snapshot.StartedAt, CompletedAt: snapshot.CompletedAt,
	}}, nil
}
