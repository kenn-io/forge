package msgvault

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// defaultTimeout caps any single upstream call. 10s is generous for a
// local msgvault on loopback; callers can provide a shorter per-request
// context deadline.
const defaultTimeout = 10 * time.Second

// Client is a typed wrapper around msgvault's HTTP API. It is safe for
// concurrent use.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient builds a Client bound to msgvault at baseURL. apiKey is sent as
// Authorization: Bearer <apiKey> on every request. The underlying http.Client
// uses defaultTimeout; callers can shorten it per-request with a context
// deadline.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: defaultTimeout},
	}
}

// Capabilities issues msgvault's two health probes: /health (reach,
// unauthenticated, body discarded) and /api/v1/stats (auth, body discarded).
// Either failure returns a typed error; nil means both succeeded.
func (c *Client) Capabilities(ctx context.Context) error {
	if err := c.probe(ctx, "/health", false); err != nil {
		return fmt.Errorf("health probe: %w", err)
	}
	if err := c.probe(ctx, "/api/v1/stats", true); err != nil {
		return fmt.Errorf("auth probe: %w", err)
	}
	return nil
}

func (c *Client) probe(ctx context.Context, path string, withAuth bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	return nil
}

func (c *Client) joinURL(path string, query url.Values) string {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

// do issues an authenticated GET and decodes the JSON response into dst.
// Non-2xx responses become *Error.
func (c *Client) do(ctx context.Context, path string, query url.Values, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.joinURL(path, query), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return decodeUpstreamError(resp.StatusCode, raw)
	}
	if dst == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func decodeUpstreamError(status int, body []byte) error {
	var env struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &env)
	if env.Message == "" {
		env.Message = string(body)
	}
	return &Error{Status: status, Code: env.Error, Message: env.Message}
}

// MessageSummary mirrors msgvault's wire shape for search/list rows.
type MessageSummary struct {
	ID             int64    `json:"id"`
	ConversationID int64    `json:"conversation_id"`
	Subject        string   `json:"subject"`
	From           string   `json:"from"`
	To             []string `json:"to"`
	CC             []string `json:"cc"`
	BCC            []string `json:"bcc"`
	SentAt         string   `json:"sent_at"`
	Snippet        string   `json:"snippet"`
	Labels         []string `json:"labels"`
	HasAttachments bool     `json:"has_attachments"`
	SizeBytes      int64    `json:"size_bytes"`
	DeletedAt      *string  `json:"deleted_at"`
}

// NormalizeSummary ensures every required field is non-nil so JSON responses
// use [] rather than null when upstream omits recipient or label slices.
func NormalizeSummary(m *MessageSummary) {
	if m.To == nil {
		m.To = []string{}
	}
	if m.CC == nil {
		m.CC = []string{}
	}
	if m.BCC == nil {
		m.BCC = []string{}
	}
	if m.Labels == nil {
		m.Labels = []string{}
	}
}

// MessageDetail extends MessageSummary with body and attachments.
type MessageDetail struct {
	MessageSummary
	Body        string           `json:"body"`
	BodyHTML    string           `json:"body_html,omitempty"`
	Attachments []AttachmentMeta `json:"attachments"`
}

type AttachmentMeta struct {
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
}

// SearchParams models msgvault's /api/v1/search query.
type SearchParams struct {
	Query    string
	Mode     string
	Page     int
	PageSize int
}

// SearchResult is what msgvault returns for /api/v1/search.
type SearchResult struct {
	Query    string           `json:"query"`
	Total    int              `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Messages []MessageSummary `json:"messages"`
}

// Search calls /api/v1/search.
func (c *Client) Search(ctx context.Context, p SearchParams) (*SearchResult, error) {
	q := url.Values{}
	q.Set("q", p.Query)
	if p.Mode != "" {
		q.Set("mode", p.Mode)
	}
	if p.Page > 0 {
		q.Set("page", fmt.Sprintf("%d", p.Page))
	}
	if p.PageSize > 0 {
		q.Set("page_size", fmt.Sprintf("%d", p.PageSize))
	}
	var out *SearchResult
	if err := c.do(ctx, "/api/v1/search", q, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	normalizeSummaries(out.Messages)
	if out.Messages == nil {
		out.Messages = []MessageSummary{}
	}
	return out, nil
}

// Message fetches /api/v1/messages/{id}.
func (c *Client) Message(ctx context.Context, id int64) (*MessageDetail, error) {
	var out *MessageDetail
	if err := c.do(ctx, fmt.Sprintf("/api/v1/messages/%d", id), nil, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	NormalizeSummary(&out.MessageSummary)
	if out.Attachments == nil {
		out.Attachments = []AttachmentMeta{}
	}
	return out, nil
}

// InlineImage returns the upstream Content-Type and an open body reader for
// the inline MIME part identified by cid. Caller must close the reader.
func (c *Client) InlineImage(ctx context.Context, id int64, cid string) (string, io.ReadCloser, error) {
	q := url.Values{"cid": {cid}}
	u := c.joinURL(fmt.Sprintf("/api/v1/messages/%d/inline", id), q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return "", nil, decodeUpstreamError(resp.StatusCode, raw)
	}
	return resp.Header.Get("Content-Type"), resp.Body, nil
}

// AggregateParams mirrors msgvault's /api/v1/aggregates query. Note that
// msgvault uses search_query, not q.
type AggregateParams struct {
	ViewType    string
	SearchQuery string
	HideDeleted bool
	Sort        string
	Direction   string
	Limit       int
}

type AggregateRow struct {
	Key             string `json:"key"`
	Count           int    `json:"count"`
	TotalSize       int64  `json:"total_size"`
	AttachmentSize  int64  `json:"attachment_size"`
	AttachmentCount int    `json:"attachment_count"`
}

type AggregateResult struct {
	ViewType string         `json:"view_type"`
	Rows     []AggregateRow `json:"rows"`
}

func (c *Client) Aggregates(ctx context.Context, p AggregateParams) (*AggregateResult, error) {
	q := url.Values{"view_type": {p.ViewType}}
	if p.SearchQuery != "" {
		q.Set("search_query", p.SearchQuery)
	}
	// Always send hide_deleted as a literal true/false so callers can override
	// upstream defaults explicitly.
	q.Set("hide_deleted", strconv.FormatBool(p.HideDeleted))
	if p.Sort != "" {
		q.Set("sort", p.Sort)
	}
	if p.Direction != "" {
		q.Set("direction", p.Direction)
	}
	if p.Limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", p.Limit))
	}
	var out *AggregateResult
	if err := c.do(ctx, "/api/v1/aggregates", q, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	if out.Rows == nil {
		out.Rows = []AggregateRow{}
	}
	return out, nil
}

// Thread returns all messages in a conversation using the sort and direction
// pinned by the route layer.
func (c *Client) Thread(ctx context.Context, conversationID int64, sort, direction string) ([]MessageSummary, error) {
	q := url.Values{
		"conversation_id": {fmt.Sprintf("%d", conversationID)},
		"sort":            {sort},
		"direction":       {direction},
	}
	var out *struct {
		Messages []MessageSummary `json:"messages"`
	}
	if err := c.do(ctx, "/api/v1/messages/filter", q, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("%w: thread returned empty response", ErrMalformedUpstream)
	}
	normalizeSummaries(out.Messages)
	if out.Messages == nil {
		out.Messages = []MessageSummary{}
	}
	return out.Messages, nil
}

func normalizeSummaries(messages []MessageSummary) {
	for i := range messages {
		NormalizeSummary(&messages[i])
	}
}
