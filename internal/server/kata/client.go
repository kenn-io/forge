package kata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	katacatalog "go.kenn.io/forge/internal/kata"
)

const (
	maxKataDaemonHealthBytes  = 1 << 20
	defaultKataReferenceLimit = 100
	maxKataReferenceLimit     = 200
)

type kataDaemonHealth struct {
	State            string
	APISchemaVersion string
}

type kataIssueReference struct {
	UID         string `json:"uid"`
	ProjectUID  string `json:"project_uid"`
	ProjectName string `json:"project_name"`
	ShortID     string `json:"short_id"`
	QualifiedID string `json:"qualified_id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
}

type kataResolvedIssueReference struct {
	UID        string `json:"uid"`
	ProjectUID string `json:"project_uid"`
}

type kataProjectReference struct {
	ID   int64  `json:"id"`
	UID  string `json:"uid"`
	Name string `json:"name"`
}

type kataReferenceQuery struct {
	Text       string
	ProjectUID string
	IssueUIDs  []string
	Limit      int
	OpenOnly   bool
}

type kataLaunchTarget struct {
	Available bool   `json:"available"`
	URL       string `json:"url,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type kataDaemonClient struct {
	daemon  katacatalog.Daemon
	client  *http.Client
	baseURL string
}

func (c *kataDaemonClient) Health(ctx context.Context) (kataDaemonHealth, error) {
	result, err := c.get(ctx, "/api/v1/health", nil, maxKataDaemonHealthBytes)
	if err != nil {
		return kataDaemonHealth{State: "down"}, err
	}
	switch result.status {
	case http.StatusUnauthorized, http.StatusForbidden:
		if isKataLocalDaemonChallenge(c.daemon, result.status) {
			return kataDaemonHealth{State: "down"}, nil
		}
		return kataDaemonHealth{State: "auth_required"}, nil
	}
	if result.status < http.StatusOK || result.status >= http.StatusMultipleChoices {
		return kataDaemonHealth{State: "down"}, fmt.Errorf(
			"kata daemon %q health returned HTTP %d", c.daemon.ID, result.status,
		)
	}
	var payload struct {
		APISchemaVersion string `json:"api_schema_version"`
	}
	if err := json.Unmarshal(result.body, &payload); err != nil {
		return kataDaemonHealth{State: "down"}, fmt.Errorf(
			"decode Kata daemon %q health response: %w", c.daemon.ID, err,
		)
	}
	return kataDaemonHealth{
		State:            "connected",
		APISchemaVersion: payload.APISchemaVersion,
	}, nil
}

func (c *kataDaemonClient) References(
	ctx context.Context,
	query kataReferenceQuery,
) ([]kataIssueReference, error) {
	requestedLimit := query.Limit
	if requestedLimit <= 0 {
		requestedLimit = defaultKataReferenceLimit
	}
	requestedLimit = min(requestedLimit, maxKataReferenceLimit)
	upstreamLimit := requestedLimit
	if query.OpenOnly {
		upstreamLimit = maxKataReferenceLimit
	}
	values := make(url.Values)
	if query.Text != "" {
		values.Set("q", query.Text)
	}
	if query.ProjectUID != "" {
		values.Set("project_uid", query.ProjectUID)
	}
	for _, issueUID := range query.IssueUIDs {
		values.Add("issue_uid", issueUID)
	}
	values.Set("limit", strconv.Itoa(upstreamLimit))
	result, err := c.get(ctx, "/api/v1/ui/references", values, maxKataDaemonReadBytes)
	if err != nil {
		return nil, err
	}
	if err := c.requireSuccess("references", result.status); err != nil {
		return nil, err
	}
	var payload struct {
		Issues []kataIssueReference `json:"issues"`
	}
	if err := json.Unmarshal(result.body, &payload); err != nil {
		return nil, fmt.Errorf(
			"decode Kata daemon %q references response: %w", c.daemon.ID, err,
		)
	}
	if !query.OpenOnly {
		return payload.Issues, nil
	}
	openIssues := make([]kataIssueReference, 0, min(requestedLimit, len(payload.Issues)))
	for _, issue := range payload.Issues {
		if issue.Status != "open" {
			continue
		}
		openIssues = append(openIssues, issue)
		if len(openIssues) == requestedLimit {
			return openIssues, nil
		}
	}
	if len(payload.Issues) == maxKataReferenceLimit {
		return nil, fmt.Errorf(
			"kata daemon %q reference window cannot prove complete open results", c.daemon.ID,
		)
	}
	return openIssues, nil
}

func (c *kataDaemonClient) IssueReferenceByUID(
	ctx context.Context,
	issueUID string,
) (kataIssueReference, bool, error) {
	values := url.Values{
		"issue_uid": []string{issueUID},
		"limit":     []string{"1"},
	}
	result, err := c.get(ctx, "/api/v1/ui/references", values, maxKataDaemonReadBytes)
	if err != nil {
		return kataIssueReference{}, false, err
	}
	if result.status == http.StatusNotFound {
		return kataIssueReference{}, false, nil
	}
	if err := c.requireSuccess("issue reference", result.status); err != nil {
		return kataIssueReference{}, false, err
	}
	var payload struct {
		Issues []kataIssueReference `json:"issues"`
	}
	if err := json.Unmarshal(result.body, &payload); err != nil {
		return kataIssueReference{}, false, fmt.Errorf(
			"decode Kata daemon %q issue reference response: %w", c.daemon.ID, err,
		)
	}
	if len(payload.Issues) != 1 || payload.Issues[0].UID != issueUID ||
		strings.TrimSpace(payload.Issues[0].ProjectUID) == "" {
		return kataIssueReference{}, false, fmt.Errorf(
			"decode Kata daemon %q issue reference response: expected canonical issue %q",
			c.daemon.ID, issueUID,
		)
	}
	return payload.Issues[0], true, nil
}

func (c *kataDaemonClient) ResolveIssueReference(
	ctx context.Context,
	project, ref string,
) (kataResolvedIssueReference, bool, error) {
	project = strings.TrimSpace(project)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return kataResolvedIssueReference{}, false, nil
	}
	if project != "" {
		return c.resolveQualifiedIssueReference(ctx, project+"#"+ref)
	}
	result, err := c.get(ctx, "/api/v1/projects", nil, maxKataDaemonReadBytes)
	if err != nil {
		return kataResolvedIssueReference{}, false, err
	}
	if err := c.requireSuccess("projects", result.status); err != nil {
		return kataResolvedIssueReference{}, false, err
	}
	var projectsPayload struct {
		Projects []kataProjectReference `json:"projects"`
	}
	if err := json.Unmarshal(result.body, &projectsPayload); err != nil {
		return kataResolvedIssueReference{}, false, fmt.Errorf(
			"decode Kata daemon %q projects response: %w", c.daemon.ID, err,
		)
	}
	var match kataResolvedIssueReference
	found := false
	for _, candidate := range projectsPayload.Projects {
		resolved, candidateFound, resolveErr := c.resolveIssueReferenceInProject(ctx, candidate.ID, ref)
		if resolveErr != nil {
			return kataResolvedIssueReference{}, false, resolveErr
		}
		if !candidateFound {
			continue
		}
		if found {
			return kataResolvedIssueReference{}, false, nil
		}
		match = resolved
		found = true
	}
	return match, found, nil
}

func (c *kataDaemonClient) resolveQualifiedIssueReference(
	ctx context.Context,
	qualifiedID string,
) (kataResolvedIssueReference, bool, error) {
	references, err := c.References(ctx, kataReferenceQuery{
		Text: qualifiedID, Limit: maxKataReferenceLimit,
	})
	if err != nil {
		return kataResolvedIssueReference{}, false, err
	}
	var match kataResolvedIssueReference
	found := false
	for _, reference := range references {
		if reference.QualifiedID != qualifiedID {
			continue
		}
		if found {
			return kataResolvedIssueReference{}, false, nil
		}
		if strings.TrimSpace(reference.UID) == "" || strings.TrimSpace(reference.ProjectUID) == "" {
			return kataResolvedIssueReference{}, false, fmt.Errorf(
				"decode Kata daemon %q qualified issue reference: missing canonical identity", c.daemon.ID,
			)
		}
		match = kataResolvedIssueReference{UID: reference.UID, ProjectUID: reference.ProjectUID}
		found = true
	}
	if !found && len(references) == maxKataReferenceLimit {
		return kataResolvedIssueReference{}, false, fmt.Errorf(
			"kata daemon %q qualified reference window is incomplete", c.daemon.ID,
		)
	}
	return match, found, nil
}

func (c *kataDaemonClient) resolveIssueReferenceInProject(
	ctx context.Context,
	projectID int64,
	ref string,
) (kataResolvedIssueReference, bool, error) {
	values := url.Values{
		"project_id": []string{strconv.FormatInt(projectID, 10)},
		"ref":        []string{ref},
	}
	result, err := c.get(ctx, "/api/v1/ui/issue-reference", values, maxKataDaemonReadBytes)
	if err != nil {
		return kataResolvedIssueReference{}, false, err
	}
	if result.status == http.StatusNotFound {
		return kataResolvedIssueReference{}, false, nil
	}
	if err := c.requireSuccess("issue reference", result.status); err != nil {
		return kataResolvedIssueReference{}, false, err
	}
	var referencePayload struct {
		Issue kataResolvedIssueReference `json:"issue"`
	}
	if err := json.Unmarshal(result.body, &referencePayload); err != nil {
		return kataResolvedIssueReference{}, false, fmt.Errorf(
			"decode Kata daemon %q issue reference response: %w", c.daemon.ID, err,
		)
	}
	if strings.TrimSpace(referencePayload.Issue.UID) == "" {
		return kataResolvedIssueReference{}, false, fmt.Errorf(
			"decode Kata daemon %q issue reference response: missing issue UID", c.daemon.ID,
		)
	}
	return referencePayload.Issue, true, nil
}

func (c *kataDaemonClient) IssueDetail(
	ctx context.Context,
	issueUID string,
) (json.RawMessage, error) {
	result, err := c.get(
		ctx,
		"/api/v1/issues/"+url.PathEscape(issueUID),
		nil,
		maxKataDaemonReadBytes,
	)
	if err != nil {
		return nil, err
	}
	if err := c.requireSuccess("issue detail", result.status); err != nil {
		return nil, err
	}
	if !json.Valid(result.body) {
		return nil, fmt.Errorf("decode Kata daemon %q issue detail response: invalid JSON", c.daemon.ID)
	}
	return json.RawMessage(result.body), nil
}

func (c *kataDaemonClient) LaunchTarget(
	ctx context.Context,
	issueUID string,
) (kataLaunchTarget, error) {
	values := url.Values{"issue_uid": []string{issueUID}}
	result, err := c.get(ctx, "/api/v1/ui/launch-target", values, maxKataDaemonReadBytes)
	if err != nil {
		return kataLaunchTarget{}, err
	}
	if err := c.requireSuccess("launch target", result.status); err != nil {
		return kataLaunchTarget{}, err
	}
	var target kataLaunchTarget
	if err := json.Unmarshal(result.body, &target); err != nil {
		return kataLaunchTarget{}, fmt.Errorf(
			"decode Kata daemon %q launch target response: %w", c.daemon.ID, err,
		)
	}
	if target.Available {
		if err := validateKataLaunchTarget(target.URL); err != nil {
			return kataLaunchTarget{}, fmt.Errorf(
				"kata daemon %q returned invalid launch target: %w", c.daemon.ID, err,
			)
		}
	}
	return target, nil
}

func validateKataLaunchTarget(rawURL string) error {
	target, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if !target.IsAbs() || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return errors.New("URL must be an absolute HTTP or HTTPS URL with a host")
	}
	if target.User != nil {
		return errors.New("URL must not contain user information")
	}
	return nil
}

func (c *kataDaemonClient) get(
	ctx context.Context,
	endpoint string,
	query url.Values,
	limit int64,
) (kataDaemonReadResult, error) {
	base, err := url.Parse(strings.TrimSuffix(c.baseURL, "/"))
	if err != nil {
		return kataDaemonReadResult{}, fmt.Errorf("build Kata daemon %q request: %w", c.daemon.ID, err)
	}
	base.User = nil
	base.RawQuery = ""
	base.Fragment = ""
	target, err := url.JoinPath(base.String(), endpoint)
	if err != nil {
		return kataDaemonReadResult{}, fmt.Errorf("build Kata daemon %q request: %w", c.daemon.ID, err)
	}
	if len(query) > 0 {
		parsed, err := url.Parse(target)
		if err != nil {
			return kataDaemonReadResult{}, fmt.Errorf("build Kata daemon %q request: %w", c.daemon.ID, err)
		}
		parsed.RawQuery = query.Encode()
		target = parsed.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return kataDaemonReadResult{}, fmt.Errorf("build Kata daemon %q request: %w", c.daemon.ID, err)
	}
	req.Header.Set("Accept", "application/json")
	if token := kataDaemonForwardToken(c.daemon); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return kataDaemonReadResult{}, ctxErr
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return kataDaemonReadResult{}, fmt.Errorf("read Kata daemon %q: %w", c.daemon.ID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ContentLength > limit {
		return kataDaemonReadResult{}, fmt.Errorf(
			"kata daemon %q response exceeds %d bytes", c.daemon.ID, limit,
		)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return kataDaemonReadResult{}, fmt.Errorf("read Kata daemon %q response: %w", c.daemon.ID, err)
	}
	if int64(len(body)) > limit {
		return kataDaemonReadResult{}, fmt.Errorf(
			"kata daemon %q response exceeds %d bytes", c.daemon.ID, limit,
		)
	}
	return kataDaemonReadResult{status: resp.StatusCode, body: body}, nil
}

func (c *kataDaemonClient) requireSuccess(operation string, status int) error {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	return fmt.Errorf(
		"kata daemon %q %s returned HTTP %d", c.daemon.ID, operation, status,
	)
}
