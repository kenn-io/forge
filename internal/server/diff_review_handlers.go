package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/platform"
)

func (s *Server) getDiffReviewDraft(
	ctx context.Context,
	input *repoNumberInput,
) (*getDiffReviewDraftOutput, error) {
	repo, mr, err := s.lookupReviewDraftTarget(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number,
	)
	if err != nil {
		return nil, err
	}
	draft, err := s.db.GetMRReviewDraft(ctx, mr.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("get review draft failed")
	}
	return &getDiffReviewDraftOutput{Body: s.diffReviewDraftResponse(*repo, draft)}, nil
}

func (s *Server) createDiffReviewDraftComment(
	ctx context.Context,
	input *createDiffReviewDraftCommentInput,
) (*createDiffReviewDraftCommentOutput, error) {
	body := strings.TrimSpace(input.Body.Body)
	if body == "" {
		return nil, huma.Error400BadRequest("comment body must not be empty")
	}
	lineRange, err := dbReviewLineRange(input.Body.Range)
	if err != nil {
		return nil, err
	}
	_, mr, err := s.lookupReviewDraftMutationTarget(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number,
	)
	if err != nil {
		return nil, err
	}
	draft, err := s.db.GetOrCreateMRReviewDraft(ctx, mr.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("create review draft failed")
	}
	if draft == nil {
		return nil, huma.Error500InternalServerError("create review draft failed")
	}
	comment, err := s.db.CreateMRReviewDraftComment(ctx, draft.ID, db.MRReviewDraftCommentInput{
		Body:  body,
		Range: lineRange,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("create review draft comment failed")
	}
	return &createDiffReviewDraftCommentOutput{
		Status: http.StatusCreated,
		Body:   diffReviewDraftCommentResponse(*comment),
	}, nil
}

func (s *Server) editDiffReviewDraftComment(
	ctx context.Context,
	input *editDiffReviewDraftCommentInput,
) (*editDiffReviewDraftCommentOutput, error) {
	body := strings.TrimSpace(input.Body.Body)
	if body == "" {
		return nil, huma.Error400BadRequest("comment body must not be empty")
	}
	lineRange, err := dbReviewLineRange(input.Body.Range)
	if err != nil {
		return nil, err
	}
	commentID, err := parseReviewLocalID(input.DraftCommentID, "draft comment")
	if err != nil {
		return nil, err
	}
	_, mr, err := s.lookupReviewDraftMutationTarget(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number,
	)
	if err != nil {
		return nil, err
	}
	draft, err := s.db.GetMRReviewDraft(ctx, mr.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("get review draft failed")
	}
	if draft == nil {
		return nil, huma.Error404NotFound("review draft not found")
	}
	comment, err := s.db.UpdateMRReviewDraftComment(ctx, draft.ID, commentID, db.MRReviewDraftCommentInput{
		Body:  body,
		Range: lineRange,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("review draft comment not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("edit review draft comment failed")
	}
	return &editDiffReviewDraftCommentOutput{Body: diffReviewDraftCommentResponse(*comment)}, nil
}

func (s *Server) deleteDiffReviewDraftComment(
	ctx context.Context,
	input *deleteDiffReviewDraftCommentInput,
) (*statusOnlyOutput, error) {
	commentID, err := parseReviewLocalID(input.DraftCommentID, "draft comment")
	if err != nil {
		return nil, err
	}
	_, mr, err := s.lookupReviewDraftMutationTarget(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number,
	)
	if err != nil {
		return nil, err
	}
	draft, err := s.db.GetMRReviewDraft(ctx, mr.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("get review draft failed")
	}
	if draft == nil {
		return nil, huma.Error404NotFound("review draft not found")
	}
	if err := s.db.DeleteMRReviewDraftComment(ctx, draft.ID, commentID); errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("review draft comment not found")
	} else if err != nil {
		return nil, huma.Error500InternalServerError("delete review draft comment failed")
	}
	return &statusOnlyOutput{Status: http.StatusOK}, nil
}

func (s *Server) discardDiffReviewDraft(
	ctx context.Context,
	input *discardDiffReviewDraftInput,
) (*statusOnlyOutput, error) {
	_, mr, err := s.lookupReviewDraftMutationTarget(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number,
	)
	if err != nil {
		return nil, err
	}
	if err := s.db.DeleteMRReviewDraft(ctx, mr.ID); err != nil {
		return nil, huma.Error500InternalServerError("discard review draft failed")
	}
	return &statusOnlyOutput{Status: http.StatusOK}, nil
}

func (s *Server) publishDiffReviewDraft(
	ctx context.Context,
	input *publishDiffReviewDraftInput,
) (*actionStatusOutput, error) {
	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityReviewDraftMutation,
	)
	if err != nil {
		return nil, err
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get pull request failed")
	}
	if mr == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}
	action, err := parseReviewAction(input.Body.Action)
	if err != nil {
		return nil, err
	}
	caps := s.capabilitiesForRepo(*repo)
	if !reviewActionSupported(caps, action) {
		return nil, huma.Error400BadRequest("unsupported review action")
	}
	draft, err := s.db.GetMRReviewDraft(ctx, mr.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("get review draft failed")
	}
	if draft == nil || len(draft.Comments) == 0 {
		return nil, huma.Error400BadRequest("review draft has no comments")
	}
	if mr.DiffHeadSHA == "" {
		return nil, huma.Error409Conflict("review diff is unavailable")
	}
	for _, comment := range draft.Comments {
		if comment.Range.DiffHeadSHA == "" || comment.Range.DiffHeadSHA != mr.DiffHeadSHA {
			return nil, huma.Error409Conflict("review draft is stale")
		}
		if !caps.NativeMultilineRanges && (comment.Range.StartLine != nil || comment.Range.StartSide != "") {
			return nil, huma.Error400BadRequest("multiline review ranges are unsupported")
		}
	}
	mutator, err := s.syncer.DiffReviewDraftMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}
	comments := make([]platform.LocalDiffReviewDraftComment, 0, len(draft.Comments))
	for _, comment := range draft.Comments {
		comments = append(comments, platform.LocalDiffReviewDraftComment{
			ID:        comment.ID,
			Body:      comment.Body,
			Range:     platformReviewLineRange(comment.Range),
			CreatedAt: comment.CreatedAt,
			UpdatedAt: comment.UpdatedAt,
		})
	}
	if _, err := mutator.PublishDiffReviewDraft(ctx, platformRepoRefFromDB(*repo), input.Number, platform.PublishDiffReviewDraftInput{
		Body:     strings.TrimSpace(input.Body.Body),
		Action:   action,
		Comments: comments,
	}); err != nil {
		return nil, huma.Error502BadGateway("publish review draft on provider failed")
	}
	if err := s.db.DeleteMRReviewDraft(ctx, mr.ID); err != nil {
		return nil, huma.Error500InternalServerError("discard published review draft failed")
	}
	if capabilityEnabled(s.capabilitiesForRepo(*repo), capabilityReadReviewThreads) {
		_ = s.ingestDiffReviewThreads(ctx, *repo, *mr)
	}
	return &actionStatusOutput{Body: actionStatusBody{Status: "published"}}, nil
}

func (s *Server) resolveDiffReviewThread(
	ctx context.Context,
	input *resolveDiffReviewThreadInput,
) (*statusOnlyOutput, error) {
	return s.setDiffReviewThreadResolved(ctx, input, true)
}

func (s *Server) unresolveDiffReviewThread(
	ctx context.Context,
	input *resolveDiffReviewThreadInput,
) (*statusOnlyOutput, error) {
	return s.setDiffReviewThreadResolved(ctx, input, false)
}

func (s *Server) setDiffReviewThreadResolved(
	ctx context.Context,
	input *resolveDiffReviewThreadInput,
	resolved bool,
) (*statusOnlyOutput, error) {
	threadID, err := parseReviewLocalID(input.ThreadID, "review thread")
	if err != nil {
		return nil, err
	}
	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityReviewThreadResolution,
	)
	if err != nil {
		return nil, err
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get pull request failed")
	}
	if mr == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}
	thread, err := s.db.GetMRReviewThread(ctx, mr.ID, threadID)
	if errors.Is(err, sql.ErrNoRows) || thread == nil {
		return nil, huma.Error404NotFound("review thread not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("get review thread failed")
	}
	resolver, err := s.syncer.DiffReviewThreadResolver(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}
	if resolved {
		err = resolver.ResolveDiffReviewThread(
			ctx, platformRepoRefFromDB(*repo), input.Number, thread.ProviderThreadID,
		)
	} else {
		err = resolver.UnresolveDiffReviewThread(
			ctx, platformRepoRefFromDB(*repo), input.Number, thread.ProviderThreadID,
		)
	}
	if err != nil {
		return nil, huma.Error502BadGateway("update review thread on provider failed")
	}
	var resolvedAt *time.Time
	if resolved {
		now := s.now().UTC()
		resolvedAt = &now
	}
	if err := s.db.SetMRReviewThreadResolved(ctx, mr.ID, thread.ID, resolved, resolvedAt); err != nil {
		return nil, huma.Error500InternalServerError("persist review thread state failed")
	}
	return &statusOnlyOutput{Status: http.StatusOK}, nil
}

func (s *Server) lookupReviewDraftTarget(
	ctx context.Context,
	provider, platformHost, owner, name string,
	number int,
) (*db.Repo, *db.MergeRequest, error) {
	repo, err := s.lookupRepoByProviderRoute(ctx, provider, platformHost, owner, name)
	if err != nil {
		return nil, nil, providerRouteLookupError(err)
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
	if err != nil {
		return nil, nil, huma.Error500InternalServerError("get pull request failed")
	}
	if mr == nil {
		return nil, nil, huma.Error404NotFound("pull request not found")
	}
	return repo, mr, nil
}

func (s *Server) lookupReviewDraftMutationTarget(
	ctx context.Context,
	provider, platformHost, owner, name string,
	number int,
) (*db.Repo, *db.MergeRequest, error) {
	repo, err := s.requireRepoRouteCapability(
		ctx,
		provider, platformHost, owner, name,
		capabilityReviewDraftMutation,
	)
	if err != nil {
		return nil, nil, err
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
	if err != nil {
		return nil, nil, huma.Error500InternalServerError("get pull request failed")
	}
	if mr == nil {
		return nil, nil, huma.Error404NotFound("pull request not found")
	}
	return repo, mr, nil
}

func (s *Server) ingestDiffReviewThreads(
	ctx context.Context,
	repo db.Repo,
	mr db.MergeRequest,
) error {
	reader, err := s.syncer.MergeRequestReviewThreadReader(
		repoProviderKind(repo), repoProviderHost(repo),
	)
	if err != nil {
		return huma.Error404NotFound(err.Error())
	}
	threads, err := reader.ListMergeRequestReviewThreads(
		ctx, platformRepoRefFromDB(repo), mr.Number,
	)
	if err != nil {
		return huma.Error502BadGateway("read review threads from provider failed")
	}
	dbThreads := make([]db.MRReviewThread, 0, len(threads))
	events := make([]db.MREvent, 0, len(threads))
	for _, thread := range threads {
		providerThreadID := thread.ProviderThreadID
		if providerThreadID == "" {
			providerThreadID = thread.ProviderCommentID
		}
		if providerThreadID == "" {
			continue
		}
		dbThread := db.MRReviewThread{
			ProviderThreadID:  providerThreadID,
			ProviderReviewID:  thread.ProviderReviewID,
			ProviderCommentID: thread.ProviderCommentID,
			Body:              thread.Body,
			AuthorLogin:       thread.AuthorLogin,
			Range:             dbReviewLineRangeFromPlatform(thread.Range),
			Resolved:          thread.Resolved,
			CreatedAt:         thread.CreatedAt,
			UpdatedAt:         thread.UpdatedAt,
			ResolvedAt:        thread.ResolvedAt,
			MetadataJSON:      thread.MetadataJSON,
		}
		dbThreads = append(dbThreads, dbThread)
		eventExternalID := providerThreadID
		if eventExternalID != "" {
			createdAt := thread.CreatedAt
			if createdAt.IsZero() {
				createdAt = s.now().UTC()
			}
			events = append(events, db.MREvent{
				MergeRequestID:     mr.ID,
				PlatformExternalID: eventExternalID,
				EventType:          "review_comment",
				Author:             thread.AuthorLogin,
				Body:               thread.Body,
				CreatedAt:          createdAt,
				DedupeKey:          "review_comment:" + eventExternalID,
			})
		}
	}
	if err := s.db.UpsertMRReviewThreads(ctx, mr.ID, dbThreads); err != nil {
		return huma.Error500InternalServerError("persist review threads failed")
	}
	if len(events) > 0 {
		if err := s.db.UpsertMREvents(ctx, events); err != nil {
			return huma.Error500InternalServerError("persist review thread events failed")
		}
	}
	return nil
}

func (s *Server) diffReviewDraftResponse(
	repo db.Repo,
	draft *db.MRReviewDraft,
) diffReviewDraftResponse {
	caps := s.capabilitiesForRepo(repo)
	resp := diffReviewDraftResponse{
		Comments:              []diffReviewDraftComment{},
		SupportedActions:      caps.SupportedReviewActions,
		NativeMultilineRanges: caps.NativeMultilineRanges,
	}
	if draft == nil {
		return resp
	}
	resp.DraftID = strconv.FormatInt(draft.ID, 10)
	resp.Comments = make([]diffReviewDraftComment, 0, len(draft.Comments))
	for _, comment := range draft.Comments {
		resp.Comments = append(resp.Comments, diffReviewDraftCommentResponse(comment))
	}
	return resp
}

func diffReviewDraftCommentResponse(comment db.MRReviewDraftComment) diffReviewDraftComment {
	lineRange := comment.Range
	return diffReviewDraftComment{
		ID:          strconv.FormatInt(comment.ID, 10),
		Body:        comment.Body,
		Path:        lineRange.Path,
		OldPath:     lineRange.OldPath,
		Side:        lineRange.Side,
		StartSide:   lineRange.StartSide,
		StartLine:   lineRange.StartLine,
		Line:        lineRange.Line,
		OldLine:     lineRange.OldLine,
		NewLine:     lineRange.NewLine,
		LineType:    lineRange.LineType,
		DiffHeadSHA: lineRange.DiffHeadSHA,
		CommitSHA:   lineRange.CommitSHA,
		CreatedAt:   formatUTCRFC3339(comment.CreatedAt),
		UpdatedAt:   formatUTCRFC3339(comment.UpdatedAt),
	}
}

func diffReviewThreadResponseFromDB(thread db.MRReviewThread) diffReviewThreadResponse {
	lineRange := thread.Range
	return diffReviewThreadResponse{
		ID:          strconv.FormatInt(thread.ID, 10),
		Path:        lineRange.Path,
		OldPath:     lineRange.OldPath,
		Side:        lineRange.Side,
		StartSide:   lineRange.StartSide,
		StartLine:   lineRange.StartLine,
		Line:        lineRange.Line,
		OldLine:     lineRange.OldLine,
		NewLine:     lineRange.NewLine,
		LineType:    lineRange.LineType,
		DiffHeadSHA: lineRange.DiffHeadSHA,
		CommitSHA:   lineRange.CommitSHA,
		Body:        thread.Body,
		AuthorLogin: thread.AuthorLogin,
		Resolved:    thread.Resolved,
		CreatedAt:   formatUTCRFC3339(thread.CreatedAt),
		UpdatedAt:   formatUTCRFC3339(thread.UpdatedAt),
	}
}

func (s *Server) mergeRequestEventResponses(
	ctx context.Context,
	mrID int64,
	events []db.MREvent,
) ([]mergeRequestEventResponse, error) {
	threads, err := s.db.ListMRReviewThreads(ctx, mrID)
	if err != nil {
		return nil, err
	}
	threadsByProviderID := make(map[string]diffReviewThreadResponse, len(threads)*2)
	for _, thread := range threads {
		resp := diffReviewThreadResponseFromDB(thread)
		if thread.ProviderThreadID != "" {
			threadsByProviderID[thread.ProviderThreadID] = resp
		}
		if thread.ProviderCommentID != "" {
			threadsByProviderID[thread.ProviderCommentID] = resp
		}
	}
	out := make([]mergeRequestEventResponse, 0, len(events))
	for _, event := range events {
		resp := mergeRequestEventResponse{MREvent: event}
		if event.EventType == "review_comment" && event.PlatformExternalID != "" {
			if thread, ok := threadsByProviderID[event.PlatformExternalID]; ok {
				resp.ReviewThread = &thread
			}
		}
		out = append(out, resp)
	}
	return out, nil
}

func dbReviewLineRange(input diffReviewLineRange) (db.ReviewLineRange, error) {
	path := strings.TrimSpace(input.Path)
	if path == "" {
		return db.ReviewLineRange{}, huma.Error400BadRequest("review range path is required")
	}
	side := strings.ToLower(strings.TrimSpace(input.Side))
	if side != "left" && side != "right" {
		return db.ReviewLineRange{}, huma.Error400BadRequest("review range side must be left or right")
	}
	if input.Line <= 0 {
		return db.ReviewLineRange{}, huma.Error400BadRequest("review range line must be positive")
	}
	lineType := strings.TrimSpace(input.LineType)
	switch lineType {
	case "context", "add", "delete":
	default:
		return db.ReviewLineRange{}, huma.Error400BadRequest(
			"review range line_type must be context, add, or delete",
		)
	}
	diffHeadSHA := strings.TrimSpace(input.DiffHeadSHA)
	if diffHeadSHA == "" {
		return db.ReviewLineRange{}, huma.Error400BadRequest("review range diff_head_sha is required")
	}
	startSide := strings.ToLower(strings.TrimSpace(input.StartSide))
	if startSide != "" && startSide != side {
		return db.ReviewLineRange{}, huma.Error400BadRequest("review range must stay on one side")
	}
	return db.ReviewLineRange{
		Path:        path,
		OldPath:     strings.TrimSpace(input.OldPath),
		Side:        side,
		StartSide:   startSide,
		StartLine:   input.StartLine,
		Line:        input.Line,
		OldLine:     input.OldLine,
		NewLine:     input.NewLine,
		LineType:    lineType,
		DiffHeadSHA: diffHeadSHA,
		CommitSHA:   strings.TrimSpace(input.CommitSHA),
	}, nil
}

func platformReviewLineRange(input db.ReviewLineRange) platform.DiffReviewLineRange {
	return platform.DiffReviewLineRange{
		Path:        input.Path,
		OldPath:     input.OldPath,
		Side:        input.Side,
		StartSide:   input.StartSide,
		StartLine:   input.StartLine,
		Line:        input.Line,
		OldLine:     input.OldLine,
		NewLine:     input.NewLine,
		LineType:    input.LineType,
		DiffHeadSHA: input.DiffHeadSHA,
		CommitSHA:   input.CommitSHA,
	}
}

func dbReviewLineRangeFromPlatform(input platform.DiffReviewLineRange) db.ReviewLineRange {
	return db.ReviewLineRange{
		Path:        input.Path,
		OldPath:     input.OldPath,
		Side:        input.Side,
		StartSide:   input.StartSide,
		StartLine:   input.StartLine,
		Line:        input.Line,
		OldLine:     input.OldLine,
		NewLine:     input.NewLine,
		LineType:    input.LineType,
		DiffHeadSHA: input.DiffHeadSHA,
		CommitSHA:   input.CommitSHA,
	}
}

func parseReviewLocalID(value, label string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, huma.Error400BadRequest(label + " id must be a positive integer")
	}
	return id, nil
}

func parseReviewAction(value string) (platform.ReviewAction, error) {
	action := platform.ReviewAction(strings.ToLower(strings.TrimSpace(value)))
	switch action {
	case platform.ReviewActionComment, platform.ReviewActionApprove, platform.ReviewActionRequestChanges:
		return action, nil
	default:
		return "", huma.Error400BadRequest("review action must be comment, approve, or request_changes")
	}
}

func reviewActionSupported(caps providerCapabilitiesResponse, action platform.ReviewAction) bool {
	return slices.Contains(caps.SupportedReviewActions, string(action))
}
