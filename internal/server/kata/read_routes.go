package kata

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/server/httpapi"
)

type kataReferencesInput struct {
	DaemonID   string   `path:"daemon_id"`
	Q          string   `query:"q"`
	ProjectUID string   `query:"project_uid"`
	IssueUIDs  []string `query:"issue_uid,explode"`
	Limit      int      `query:"limit" minimum:"0"`
}

type kataReferencesResponse struct {
	Issues []kataIssueReference `json:"issues" nullable:"false"`
}

type kataReferencesOutput = httpapi.BodyOutput[kataReferencesResponse]

type kataIssueReferenceResolveInput struct {
	DaemonID string `path:"daemon_id"`
	Project  string `query:"project"`
	Ref      string `query:"ref" required:"true"`
}

type kataIssueReferenceResolveOutput = httpapi.BodyOutput[kataResolvedIssueReference]

type kataIssueReadInput struct {
	DaemonID string `path:"daemon_id"`
	IssueUID string `path:"issue_uid"`
}

type kataIssueDetailResponse struct {
	DaemonHealth     string          `json:"daemon_health"`
	APISchemaVersion string          `json:"api_schema_version,omitempty"`
	Detail           json.RawMessage `json:"detail"`
}

func (*kataIssueDetailResponse) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	if schema == nil {
		return nil
	}
	if schema.Properties == nil {
		schema.Properties = map[string]*huma.Schema{}
	}
	schema.Properties["detail"] = &huma.Schema{
		Type:                 "object",
		AdditionalProperties: true,
		Description:          "Canonical Kata issue detail. Additive fields are preserved.",
	}
	return schema
}

type kataIssueDetailOutput = httpapi.BodyOutput[kataIssueDetailResponse]
type kataLaunchTargetOutput = httpapi.BodyOutput[kataLaunchTarget]

func registerKataReadAPI(api huma.API, h *Handler) {
	huma.Get(api, "/kata/daemons/{daemon_id}/references", h.listKataReferences,
		httpapi.DocumentOperation("list-kata-references", "List Kata issue references", "Kata"))
	huma.Get(api, "/kata/daemons/{daemon_id}/issue-reference", h.resolveKataIssueReference,
		httpapi.DocumentOperation("resolve-kata-issue-reference", "Resolve a Kata issue reference", "Kata"))
	huma.Get(api, "/kata/daemons/{daemon_id}/issues/{issue_uid}", h.getKataIssueDetail,
		httpapi.DocumentOperation("get-kata-issue-detail", "Get Kata issue detail", "Kata"))
	huma.Get(api, "/kata/daemons/{daemon_id}/issues/{issue_uid}/launch-target", h.getKataLaunchTarget,
		httpapi.DocumentOperation("get-kata-launch-target", "Get Kata issue launch target", "Kata"))
}

func (h *Handler) listKataReferences(
	ctx context.Context,
	input *kataReferencesInput,
) (*kataReferencesOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, kataDaemonReadTimeout)
	defer cancel()
	client, problem := h.kataClientForDaemon(input.DaemonID)
	if problem != nil {
		return nil, problem
	}
	issues, err := client.References(ctx, kataReferenceQuery{
		Text: input.Q, ProjectUID: input.ProjectUID,
		IssueUIDs: input.IssueUIDs, Limit: input.Limit,
		OpenOnly: len(input.IssueUIDs) == 0,
	})
	if err != nil {
		return nil, kataUpstreamProblem("read Kata issue references failed", input.DaemonID)
	}
	if issues == nil {
		issues = []kataIssueReference{}
	}
	return &kataReferencesOutput{Body: kataReferencesResponse{Issues: issues}}, nil
}

func (h *Handler) resolveKataIssueReference(
	ctx context.Context,
	input *kataIssueReferenceResolveInput,
) (*kataIssueReferenceResolveOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, kataDaemonReadTimeout)
	defer cancel()
	client, problem := h.kataClientForDaemon(input.DaemonID)
	if problem != nil {
		return nil, problem
	}
	reference, found, err := client.ResolveIssueReference(ctx, input.Project, input.Ref)
	if err != nil {
		return nil, kataUpstreamProblem("resolve Kata issue reference failed", input.DaemonID)
	}
	if !found {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "Kata issue reference not found", nil)
	}
	return &kataIssueReferenceResolveOutput{Body: reference}, nil
}

func (h *Handler) getKataIssueDetail(
	ctx context.Context,
	input *kataIssueReadInput,
) (*kataIssueDetailOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, kataDaemonReadTimeout)
	defer cancel()
	client, problem := h.kataClientForDaemon(input.DaemonID)
	if problem != nil {
		return nil, problem
	}
	health, err := client.Health(ctx)
	if err != nil || health.State != "connected" {
		return nil, kataDaemonUnavailableProblem(input.DaemonID, health.State)
	}
	detail, err := client.IssueDetail(ctx, input.IssueUID)
	if err != nil {
		return nil, kataUpstreamProblem("read Kata issue detail failed", input.DaemonID)
	}
	return &kataIssueDetailOutput{Body: kataIssueDetailResponse{
		DaemonHealth: health.State, APISchemaVersion: health.APISchemaVersion, Detail: detail,
	}}, nil
}

func (h *Handler) getKataLaunchTarget(
	ctx context.Context,
	input *kataIssueReadInput,
) (*kataLaunchTargetOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, kataDaemonReadTimeout)
	defer cancel()
	client, problem := h.kataClientForDaemon(input.DaemonID)
	if problem != nil {
		return nil, problem
	}
	target, err := client.LaunchTarget(ctx, input.IssueUID)
	if err != nil {
		return nil, kataUpstreamProblem("read Kata launch target failed", input.DaemonID)
	}
	return &kataLaunchTargetOutput{Body: target}, nil
}

func (h *Handler) kataClientForDaemon(daemonID string) (*kataDaemonClient, *httpapi.ProblemError) {
	if strings.TrimSpace(daemonID) == "" {
		return nil, httpapi.NewProblem(
			http.StatusBadRequest,
			httpapi.CodeValidationError,
			"daemon_id is required",
			map[string]any{"field": "path.daemon_id"},
		)
	}
	daemon, problem := h.selectKataDaemonForID(daemonID)
	if problem != nil {
		return nil, problem
	}
	client, baseURL, err := h.kataDaemonHTTPClient(daemon)
	if err != nil {
		return nil, httpapi.NewProblem(
			http.StatusServiceUnavailable,
			httpapi.CodeServiceUnavailable,
			"Kata daemon transport is unavailable",
			map[string]any{"daemon": daemonID},
		)
	}
	return &kataDaemonClient{daemon: daemon, client: client, baseURL: baseURL}, nil
}

func kataDaemonUnavailableProblem(daemonID, health string) *httpapi.ProblemError {
	details := map[string]any{"daemon": daemonID}
	if health != "" {
		details["health"] = health
	}
	return httpapi.NewProblem(
		http.StatusServiceUnavailable,
		httpapi.CodeServiceUnavailable,
		"Kata daemon is unavailable",
		details,
	)
}

func kataUpstreamProblem(detail, daemonID string) *httpapi.ProblemError {
	return httpapi.NewProblem(
		http.StatusBadGateway,
		httpapi.CodeUpstreamError,
		detail,
		map[string]any{"daemon": daemonID},
	)
}
