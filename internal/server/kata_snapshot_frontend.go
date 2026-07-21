package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/kata"
)

const (
	kataSnapshotDeliveryAttempts = 2
	kataReferenceDefaultLimit    = 20
	kataReferenceMaxLimit        = 50
)

var errKataSnapshotDeliveryStale = errors.New("kata snapshot changed before delivery")

type kataFrontendEventHandle interface {
	DaemonFingerprint() string
	Cursor() uint64
}

type kataTaskSnapshotInput struct {
	DaemonID         string `header:"X-Middleman-Kata-Daemon" doc:"Kata daemon id; the effective default daemon when empty"`
	Scope            string `query:"scope" default:"global" enum:"global,project"`
	ProjectUID       string `query:"project_uid"`
	Authority        string `query:"authority" default:"open" enum:"open,ready,closed,all"`
	SelectedIssueUID string `query:"selected_issue_uid"`
	GraphSourceUID   string `query:"graph_source_uid"`
}

type kataTaskSnapshotResponse struct {
	ServerInstanceID  string                 `json:"server_instance_id"`
	DaemonID          string                 `json:"daemon_id"`
	Intent            kataAuthorityRequest   `json:"intent"`
	Generation        uint64                 `json:"generation"`
	InvalidationEpoch uint64                 `json:"invalidation_epoch"`
	EventCursor       uint64                 `json:"event_cursor"`
	FetchedAt         time.Time              `json:"fetched_at"`
	Projects          []kataProjectSummary   `json:"projects"`
	MemberIssueUIDs   []string               `json:"member_issue_uids"`
	Issues            []kataTaskSummary      `json:"issues"`
	Enrichment        kataSnapshotEnrichment `json:"enrichment"`
}

type kataTaskSnapshotOutput struct {
	Vary string `header:"Vary"`
	Body kataTaskSnapshotResponse
}

type kataTaskReferenceInput struct {
	DaemonID string `header:"X-Middleman-Kata-Daemon" doc:"Kata daemon id; the effective default daemon when empty"`
	Query    string `query:"q"`
	Limit    int    `query:"limit" default:"20" minimum:"1" maximum:"50"`
}

type kataTaskReference struct {
	UID         string `json:"uid"`
	ProjectID   int64  `json:"project_id"`
	ProjectUID  string `json:"project_uid"`
	ProjectName string `json:"project_name"`
	ShortID     string `json:"short_id"`
	QualifiedID string `json:"qualified_id"`
	Title       string `json:"title"`
	Reference   string `json:"reference"`
}

type kataTaskReferenceResponse struct {
	ServerInstanceID  string              `json:"server_instance_id"`
	DaemonID          string              `json:"daemon_id"`
	Generation        uint64              `json:"generation"`
	InvalidationEpoch uint64              `json:"invalidation_epoch"`
	FetchedAt         time.Time           `json:"fetched_at"`
	References        []kataTaskReference `json:"references"`
}

type kataTaskReferenceOutput struct {
	Vary string `header:"Vary"`
	Body kataTaskReferenceResponse
}

type kataSnapshotFrontendDeps struct {
	resolveDaemon func(string) (kata.Daemon, *ProblemError)
	ensureEvents  func(kata.Daemon) (kataFrontendEventHandle, error)
	loadAuthority func(context.Context, string, kataAuthorityRequest) (kataCoordinatedAuthority, error)
	daemonEpoch   func(string) uint64
	newClient     func(context.Context, kata.Daemon) (kataAPIClient, error)
	enrich        func(context.Context, kataAPIClient, kataCoordinatedAuthority, kataSnapshotEnrichmentRequest) (kataSnapshotEnrichment, error)
}

type kataSnapshotFrontend struct {
	deps kataSnapshotFrontendDeps
}

func newKataSnapshotFrontend(deps kataSnapshotFrontendDeps) *kataSnapshotFrontend {
	return &kataSnapshotFrontend{deps: deps}
}

func (s *Server) kataSnapshotFrontend() *kataSnapshotFrontend {
	return newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: selectKataDaemonForID,
		ensureEvents: func(daemon kata.Daemon) (kataFrontendEventHandle, error) {
			return s.kataEvents.Ensure(daemon)
		},
		loadAuthority: s.kataSnapshots.loadAuthority,
		daemonEpoch:   s.kataSnapshots.daemonEpoch,
		newClient:     newKataAPIClient,
		enrich: func(
			ctx context.Context,
			client kataAPIClient,
			authority kataCoordinatedAuthority,
			request kataSnapshotEnrichmentRequest,
		) (kataSnapshotEnrichment, error) {
			enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{
				client: client,
				resolveWorkspaceTarget: func(ctx context.Context, metadata db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error) {
					return s.kataWorkspaceTargetForMetadata(ctx, metadata)
				},
			})
			return enricher.Enrich(ctx, authority, request)
		},
	})
}

func (s *Server) kataTaskSnapshot(
	ctx context.Context,
	input *kataTaskSnapshotInput,
) (out *kataTaskSnapshotOutput, err error) {
	defer func() {
		if err != nil {
			err = huma.ErrorWithHeaders(err, http.Header{"Vary": []string{kataDaemonHeaderName}})
		}
	}()
	response, err := s.kataSnapshotFrontend().Snapshot(ctx, input)
	if err != nil {
		return nil, err
	}
	return &kataTaskSnapshotOutput{Vary: kataDaemonHeaderName, Body: response}, nil
}

func (s *Server) kataTaskReferences(
	ctx context.Context,
	input *kataTaskReferenceInput,
) (out *kataTaskReferenceOutput, err error) {
	defer func() {
		if err != nil {
			err = huma.ErrorWithHeaders(err, http.Header{"Vary": []string{kataDaemonHeaderName}})
		}
	}()
	response, err := s.kataSnapshotFrontend().References(ctx, input)
	if err != nil {
		return nil, err
	}
	return &kataTaskReferenceOutput{Vary: kataDaemonHeaderName, Body: response}, nil
}

func (f *kataSnapshotFrontend) Snapshot(
	ctx context.Context,
	input *kataTaskSnapshotInput,
) (kataTaskSnapshotResponse, error) {
	intent := kataAuthorityRequest{Scope: input.Scope, ProjectUID: input.ProjectUID, Authority: input.Authority}
	if intent.Scope == "" {
		intent.Scope = "global"
	}
	if intent.Authority == "" {
		intent.Authority = "open"
	}
	if err := validateKataAuthorityRequest(intent); err != nil {
		return kataTaskSnapshotResponse{}, err
	}
	enrichmentRequest := kataSnapshotEnrichmentRequest{
		SelectedIssueUID: strings.TrimSpace(input.SelectedIssueUID),
		GraphSourceUID:   strings.TrimSpace(input.GraphSourceUID),
	}

	for range kataSnapshotDeliveryAttempts {
		if err := ctx.Err(); err != nil {
			return kataTaskSnapshotResponse{}, err
		}
		daemon, problem := f.deps.resolveDaemon(input.DaemonID)
		if problem != nil {
			return kataTaskSnapshotResponse{}, problem
		}
		binding, err := f.deps.ensureEvents(daemon)
		if err != nil {
			return kataTaskSnapshotResponse{}, problemServiceUnavailable("Kata task events are unavailable while the server is shutting down")
		}
		authority, err := f.deps.loadAuthority(ctx, daemon.ID, intent)
		if err != nil {
			return kataTaskSnapshotResponse{}, err
		}
		acceptedDaemon, problem := f.deps.resolveDaemon(authority.DaemonID)
		if problem != nil || binding.DaemonFingerprint() != authority.Key.DaemonFingerprint ||
			kataDaemonTargetFingerprint(acceptedDaemon) != authority.Key.DaemonFingerprint {
			continue
		}
		client, err := f.deps.newClient(ctx, acceptedDaemon)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return kataTaskSnapshotResponse{}, contextErr
			}
			return kataTaskSnapshotResponse{}, kataSnapshotUpstreamError("create enrichment client", err)
		}
		enrichment, err := f.deps.enrich(ctx, client, authority, enrichmentRequest)
		if err != nil {
			return kataTaskSnapshotResponse{}, err
		}
		if err := ctx.Err(); err != nil {
			return kataTaskSnapshotResponse{}, err
		}
		cursor := binding.Cursor()
		configuredDaemon, problem := f.deps.resolveDaemon(authority.DaemonID)
		if problem != nil || binding.DaemonFingerprint() != authority.Key.DaemonFingerprint ||
			kataDaemonTargetFingerprint(configuredDaemon) != authority.Key.DaemonFingerprint ||
			f.deps.daemonEpoch(authority.DaemonID) != authority.InvalidationEpoch {
			continue
		}
		return kataTaskSnapshotResponse{
			ServerInstanceID: authority.ServerInstanceID, DaemonID: authority.DaemonID, Intent: intent,
			Generation: authority.Generation, InvalidationEpoch: authority.InvalidationEpoch,
			EventCursor: cursor, FetchedAt: authority.Snapshot.FetchedAt,
			Projects: authority.Snapshot.Projects, MemberIssueUIDs: authority.Snapshot.MemberIssueUIDs,
			Issues: authority.Snapshot.Issues, Enrichment: enrichment,
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return kataTaskSnapshotResponse{}, err
	}
	return kataTaskSnapshotResponse{}, kataSnapshotUpstreamError("deliver consistent snapshot", errKataSnapshotDeliveryStale)
}

func (f *kataSnapshotFrontend) References(
	ctx context.Context,
	input *kataTaskReferenceInput,
) (kataTaskReferenceResponse, error) {
	limit := input.Limit
	if limit == 0 {
		limit = kataReferenceDefaultLimit
	}
	if limit < 1 || limit > kataReferenceMaxLimit {
		return kataTaskReferenceResponse{}, problemValidation("limit", "limit must be between 1 and 50")
	}
	authority, err := f.deps.loadAuthority(ctx, input.DaemonID, kataAuthorityRequest{Scope: "global", Authority: "open"})
	if err != nil {
		return kataTaskReferenceResponse{}, err
	}
	memberUIDs := make(map[string]struct{}, len(authority.Snapshot.MemberIssueUIDs))
	for _, uid := range authority.Snapshot.MemberIssueUIDs {
		memberUIDs[uid] = struct{}{}
	}
	shortIDCounts := make(map[string]int, len(authority.Snapshot.Issues))
	for _, issue := range authority.Snapshot.Issues {
		if _, member := memberUIDs[issue.UID]; member {
			shortIDCounts[issue.ShortID]++
		}
	}
	query := strings.ToLower(strings.TrimSpace(input.Query))
	references := make([]kataTaskReference, 0, min(limit, len(authority.Snapshot.Issues)))
	for _, issue := range authority.Snapshot.Issues {
		if _, member := memberUIDs[issue.UID]; !member || !kataReferenceMatches(issue, query) {
			continue
		}
		reference := issue.QualifiedID
		if shortIDCounts[issue.ShortID] == 1 {
			reference = issue.ShortID
		}
		references = append(references, kataTaskReference{
			UID: issue.UID, ProjectID: issue.ProjectID, ProjectUID: issue.ProjectUID,
			ProjectName: issue.ProjectName, ShortID: issue.ShortID, QualifiedID: issue.QualifiedID,
			Title: issue.Title, Reference: reference,
		})
		if len(references) == limit {
			break
		}
	}
	return kataTaskReferenceResponse{
		ServerInstanceID: authority.ServerInstanceID, DaemonID: authority.DaemonID,
		Generation: authority.Generation, InvalidationEpoch: authority.InvalidationEpoch,
		FetchedAt: authority.Snapshot.FetchedAt, References: references,
	}, nil
}

func kataReferenceMatches(issue kataTaskSummary, query string) bool {
	if query == "" {
		return true
	}
	for _, candidate := range []string{issue.ShortID, issue.QualifiedID, issue.Title, issue.ProjectName, issue.UID} {
		if strings.Contains(strings.ToLower(candidate), query) {
			return true
		}
	}
	return false
}
