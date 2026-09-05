package githubapp

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go.kenn.io/forge/platform"
)

// PageQuery requests one page. Cursor is bound to the instance, credential
// scope, and page size. Callers share a Meter across a complete discovery.
type PageQuery struct {
	Size   int
	Cursor string
}

type Repository struct {
	ID            int64   `json:"id"`
	FullName      string  `json:"full_name"`
	Name          string  `json:"name"`
	Owner         Account `json:"owner"`
	DefaultBranch string  `json:"default_branch"`
	CloneURL      string  `json:"clone_url"`
}

type pageCursor struct {
	Host  string `json:"host"`
	Path  string `json:"path"`
	Scope int64  `json:"scope"`
	Size  int    `json:"size"`
	Page  int    `json:"page"`
}

func (c *Client) preparePage(path string, scope int64, query PageQuery) (pageCursor, error) {
	current := pageCursor{Host: c.host, Path: path, Scope: scope, Size: query.Size, Page: 1}
	if scope <= 0 || query.Size < 1 || query.Size > 100 {
		return current, c.pageError(platform.ErrCodeInvalidArgument, "page_query")
	}
	if query.Cursor != "" {
		data, err := base64.RawURLEncoding.Strict().DecodeString(query.Cursor)
		if err != nil || json.Unmarshal(data, &current) != nil || current.Host != c.host || current.Path != path || current.Scope != scope || current.Size != query.Size || current.Page < 2 {
			return current, c.pageError(platform.ErrCodeInvalidArgument, "cursor")
		}
	}
	return current, nil
}

func (c *Client) pageError(code platform.PlatformErrorCode, field string) error {
	return &platform.Error{Code: code, Provider: platform.KindGitHub, PlatformHost: c.host, Field: field}
}

// nextPage accepts only a locally reproducible next page, never an arbitrary
// provider-supplied URL. Missing Link means exhaustion, not a guessed full page.
func (c *Client) nextPage(current pageCursor, header http.Header) (string, error) {
	var next string
	for part := range strings.SplitSeq(header.Get("Link"), ",") {
		link, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok {
			continue
		}
		_, attributes, err := mime.ParseMediaType("link;" + params)
		if err != nil {
			return "", c.pageError(platform.ErrCodeProviderContract, "next_page")
		}
		if attributes["rel"] != "next" {
			continue
		}
		parsed, err := url.Parse(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(link), "<"), ">"))
		expected, parseErr := url.Parse(c.apiBase + current.Path)
		if err != nil || parseErr != nil || parsed.Scheme != expected.Scheme || parsed.Host != expected.Host || parsed.Path != expected.Path || parsed.User != nil || parsed.Fragment != "" || next != "" {
			return "", c.pageError(platform.ErrCodeProviderContract, "next_page")
		}
		page, err := strconv.Atoi(parsed.Query().Get("page"))
		if err != nil || page <= current.Page || page-current.Page != 1 || parsed.Query().Get("per_page") != strconv.Itoa(current.Size) {
			return "", c.pageError(platform.ErrCodeProviderContract, "next_page")
		}
		current.Page = page
		encoded, err := json.Marshal(current)
		if err != nil {
			return "", err
		}
		next = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return next, nil
}

func publishPage[T any](items []T, next string, meter *platform.Meter) (platform.Page[T], error) {
	page := platform.Page[T]{Items: items, NextCursor: next, Exhausted: next == ""}
	if err := meter.CheckOutput(page); err != nil {
		return platform.Page[T]{}, err
	}
	return page, nil
}

func (c *Client) ListInstallationsPage(ctx context.Context, jwt string, appID int64, query PageQuery, meter *platform.Meter) (platform.Page[Installation], error) {
	var empty platform.Page[Installation]
	current, err := c.preparePage("/app/installations", appID, query)
	if err != nil {
		return empty, err
	}
	header, data, err := c.request(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=%d&page=%d", current.Path, current.Size, current.Page), jwt, nil, meter)
	if err != nil {
		return empty, fmt.Errorf("listing app installations: %w", err)
	}
	items, err := platform.DecodeEvidencePage[Installation](data, query.Size, meter)
	if err != nil {
		return empty, err
	}
	for _, item := range items {
		if item.ID <= 0 || item.AppID != appID || item.Account.ID <= 0 {
			return empty, c.pageError(platform.ErrCodeProviderContract, "installation_identity")
		}
	}
	next, err := c.nextPage(current, header)
	if err != nil {
		return empty, err
	}
	return publishPage(items, next, meter)
}

func (c *Client) ListInstallationRepositoriesPage(ctx context.Context, token string, installationID int64, query PageQuery, meter *platform.Meter) (platform.Page[Repository], error) {
	var empty platform.Page[Repository]
	current, err := c.preparePage("/installation/repositories", installationID, query)
	if err != nil {
		return empty, err
	}
	header, data, err := c.request(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=%d&page=%d", current.Path, current.Size, current.Page), token, nil, meter)
	if err != nil {
		return empty, fmt.Errorf("listing installation repositories: %w", err)
	}
	var envelope struct {
		TotalCount   *int64         `json:"total_count"`
		Repositories jsontext.Value `json:"repositories"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return empty, err
	}
	items, err := platform.DecodeEvidencePage[Repository](envelope.Repositories, query.Size, meter)
	if err != nil {
		return empty, err
	}
	for _, item := range items {
		if item.ID <= 0 || item.FullName == "" {
			return empty, c.pageError(platform.ErrCodeProviderContract, "repository_identity")
		}
	}
	next, err := c.nextPage(current, header)
	if err != nil {
		return empty, err
	}
	if envelope.TotalCount == nil || *envelope.TotalCount < 0 || (next == "" && *envelope.TotalCount > int64(current.Page-1)*int64(current.Size)+int64(len(items))) {
		return empty, c.pageError(platform.ErrCodeProviderContract, "repository_total")
	}
	return publishPage(items, next, meter)
}
