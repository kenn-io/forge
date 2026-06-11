package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/messages/msgvault"
)

const msgvaultCapabilityCacheTTL = 5 * time.Second
const msgvaultConfigureMaxBodyBytes = 4 << 20
const msgvaultInlineMaxBytes = 5 * 1024 * 1024

type msgvaultHealthBody struct {
	Configured   bool           `json:"configured"`
	OK           bool           `json:"ok"`
	Status       string         `json:"status,omitempty"`
	StatusDetail string         `json:"status_detail,omitempty"`
	Modes        []string       `json:"modes"`
	Features     map[string]any `json:"features"`
	URL          string         `json:"url,omitempty"`
	APIKeyEnv    string         `json:"api_key_env,omitempty"`
}

type msgvaultSearchInput struct {
	Q        string `query:"q"`
	Mode     string `query:"mode"`
	Page     int    `query:"page"`
	PageSize int    `query:"page_size"`
}

type msgvaultSearchBody struct {
	Query       string                    `json:"query"`
	Mode        string                    `json:"mode"`
	Total       int                       `json:"total"`
	Page        int                       `json:"page"`
	PageSize    int                       `json:"page_size"`
	Paginatable bool                      `json:"paginatable"`
	Messages    []msgvault.MessageSummary `json:"messages"`
}

type msgvaultAggregatesInput struct {
	ViewType    string `query:"view_type"`
	Q           string `query:"q"`
	HideDeleted string `query:"hide_deleted"`
	Sort        string `query:"sort"`
	Direction   string `query:"direction"`
	Limit       int    `query:"limit"`
}

type msgvaultMessageInput struct {
	ID int64 `path:"id"`
}

type msgvaultMessageBody struct {
	ID                     int64                     `json:"id"`
	ConversationID         int64                     `json:"conversation_id"`
	Subject                string                    `json:"subject"`
	From                   string                    `json:"from"`
	To                     []string                  `json:"to"`
	CC                     []string                  `json:"cc"`
	BCC                    []string                  `json:"bcc"`
	SentAt                 string                    `json:"sent_at"`
	Snippet                string                    `json:"snippet"`
	Labels                 []string                  `json:"labels"`
	HasAttachments         bool                      `json:"has_attachments"`
	SizeBytes              int64                     `json:"size_bytes"`
	DeletedAt              *string                   `json:"deleted_at"`
	Body                   string                    `json:"body"`
	Attachments            []msgvault.AttachmentMeta `json:"attachments"`
	BodyHTML               string                    `json:"body_html,omitempty"`
	RemoteImageCount       int                       `json:"remote_image_count,omitempty"`
	RemoteImageToken       string                    `json:"remote_image_token,omitempty"`
	HTMLSanitizationFailed bool                      `json:"html_sanitization_failed,omitempty"`
}

type msgvaultInlineInput struct {
	ID  int64  `path:"id"`
	CID string `query:"cid"`
}

type msgvaultRemoteImageInput struct {
	ID    int64  `path:"id"`
	Token string `path:"token"`
	Idx   int    `path:"idx"`
}

type msgvaultRawImageOutput struct {
	ContentType        string `header:"Content-Type"`
	ContentTypeOptions string `header:"X-Content-Type-Options"`
	CacheControl       string `header:"Cache-Control"`
	ContentLength      string `header:"Content-Length"`
	Body               []byte
}

type msgvaultThreadInput struct {
	ConversationID int64 `path:"conversation_id"`
}

type msgvaultThreadBody struct {
	ConversationID int64                     `json:"conversation_id"`
	Messages       []msgvault.MessageSummary `json:"messages"`
}

var msgvaultInlineSafeContentTypes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
}

type msgvaultHealthOutput = bodyOutput[msgvaultHealthBody]
type msgvaultConfigureOutput = bodyOutput[msgvaultHealthBody]
type msgvaultSearchOutput = bodyOutput[msgvaultSearchBody]
type msgvaultAggregatesOutput = bodyOutput[msgvault.AggregateResult]
type msgvaultMessageOutput = bodyOutput[msgvaultMessageBody]
type msgvaultThreadOutput = bodyOutput[msgvaultThreadBody]

type configureMsgvaultInput struct {
	ContentType   string `header:"Content-Type"`
	MiddlemanCSRF string `header:"X-Middleman-Csrf" required:"true"`
	RawBody       []byte
}

type msgvaultConfigureRequest struct {
	URL       string `json:"url"`
	APIKeyEnv string `json:"api_key_env"`
}

type msgvaultHandler struct {
	mu            sync.Mutex
	state         config.MsgvaultState
	configErr     error
	client        *msgvault.Client
	configuredURL string
	configuredEnv string
	gen           uint64
	sanitizer     *msgvault.Sanitizer
	remoteDeps    msgvaultRemoteImageDeps

	capMu       sync.Mutex
	capCache    *msgvaultHealthBody
	capExpires  time.Time
	capCacheGen uint64

	savedSearchesMu sync.Mutex
}

type msgvaultHealthSnapshot struct {
	state         config.MsgvaultState
	configErr     error
	client        *msgvault.Client
	configuredURL string
	configuredEnv string
	gen           uint64
}

func newMsgvaultHandler(cfg *config.Config, basePath string, remoteDeps *msgvaultRemoteImageDeps) *msgvaultHandler {
	h := &msgvaultHandler{remoteDeps: defaultMsgvaultRemoteImageDeps()}
	if remoteDeps != nil {
		h.remoteDeps = *remoteDeps
	}
	h.sanitizer = msgvault.NewSanitizerForBasePath(basePath)
	h.applyConfig(cfg)
	return h
}

func (h *msgvaultHandler) applyConfig(cfg *config.Config) {
	state, upstreamURL, apiKey, cfgErr := cfg.MsgvaultState()
	configuredURL := upstreamURL
	configuredEnv := ""
	if cfg != nil && cfg.Msgvault != nil {
		if configuredURL == "" {
			configuredURL = strings.TrimSpace(cfg.Msgvault.URL)
		}
		configuredEnv = strings.TrimSpace(cfg.Msgvault.APIKeyEnv)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = state
	h.configErr = cfgErr
	h.configuredURL = configuredURL
	h.configuredEnv = configuredEnv
	h.client = nil
	if state == config.MsgvaultOK {
		h.client = msgvault.NewClient(upstreamURL, apiKey)
	}
	if h.sanitizer == nil {
		h.sanitizer = msgvault.NewSanitizerForBasePath("/")
	} else {
		h.sanitizer.BumpGeneration()
	}
	h.gen++
}

func (s *Server) registerMsgvaultAPI(api huma.API) {
	huma.Get(api, "/msgvault/health", s.msgvault.health,
		documentOperation("get-msgvault-health", "Get msgvault health", "Msgvault"))
	huma.Get(api, "/msgvault/search", s.msgvault.search,
		documentOperation("search-msgvault", "Search msgvault", "Msgvault"))
	huma.Get(api, "/msgvault/messages/{id}", s.msgvault.message,
		documentOperation("get-msgvault-message", "Get msgvault message", "Msgvault"))
	huma.Get(api, "/msgvault/messages/{id}/inline", s.msgvault.inline,
		documentOperation("get-msgvault-inline-image", "Get msgvault inline image", "Msgvault"))
	huma.Get(api, "/msgvault/messages/{id}/remote-image/{token}/{idx}", s.msgvault.remoteImage,
		documentOperation("get-msgvault-remote-image", "Get msgvault remote image", "Msgvault"))
	huma.Get(api, "/msgvault/aggregates", s.msgvault.aggregates,
		documentOperation("get-msgvault-aggregates", "Get msgvault aggregates", "Msgvault"))
	huma.Get(api, "/msgvault/threads/{conversation_id}", s.msgvault.thread,
		documentOperation("get-msgvault-thread", "Get msgvault thread", "Msgvault"))
	huma.Register(api, huma.Operation{
		OperationID:   "configure-msgvault",
		Method:        http.MethodPost,
		Path:          "/msgvault/configure",
		DefaultStatus: http.StatusOK,
		Summary:       "Configure msgvault",
		Tags:          []string{"Msgvault"},
		MaxBodyBytes:  msgvaultConfigureMaxBodyBytes,
	}, s.configureMsgvault)
	documentMsgvaultConfigureJSONBody(api)
	documentMsgvaultRequiredQuery(api, "/msgvault/aggregates", "view_type")
	documentMsgvaultRawImageRoute(api, "/msgvault/messages/{id}/inline", "cid")
	documentMsgvaultRawImageRoute(api, "/msgvault/messages/{id}/remote-image/{token}/{idx}", "")
}

func (h *msgvaultHandler) health(context.Context, *struct{}) (*msgvaultHealthOutput, error) {
	return &msgvaultHealthOutput{Body: h.healthValue()}, nil
}

func (h *msgvaultHandler) search(ctx context.Context, in *msgvaultSearchInput) (*msgvaultSearchOutput, error) {
	client, prob := h.requireConfigured()
	if prob != nil {
		return nil, prob
	}
	mode := in.Mode
	if mode == "" {
		mode = "fts"
	}
	if mode != "fts" {
		return nil, problemBadRequest(
			CodeBadRequest,
			"only mode=fts is supported",
			map[string]any{"reason": "modeUnsupported"},
		)
	}
	page := in.Page
	if page <= 0 {
		page = 1
	}
	pageSize := in.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}

	result, err := client.Search(ctx, msgvault.SearchParams{
		Query:    in.Q,
		Mode:     mode,
		Page:     page,
		PageSize: pageSize,
	})
	if prob := msgvaultUpstreamProblem(err); prob != nil {
		return nil, prob
	}
	if result == nil {
		return nil, msgvaultMalformedUpstreamProblem("msgvault search returned an empty response")
	}
	return &msgvaultSearchOutput{Body: msgvaultSearchBody{
		Query:       result.Query,
		Mode:        mode,
		Total:       result.Total,
		Page:        result.Page,
		PageSize:    result.PageSize,
		Paginatable: true,
		Messages:    result.Messages,
	}}, nil
}

func (h *msgvaultHandler) message(ctx context.Context, in *msgvaultMessageInput) (*msgvaultMessageOutput, error) {
	client, sanitizer, gen, prob := h.requireConfiguredWithSanitizer()
	if prob != nil {
		return nil, prob
	}
	detail, err := client.Message(ctx, in.ID)
	if prob := msgvaultUpstreamProblem(err); prob != nil {
		return nil, prob
	}
	if detail == nil {
		return nil, msgvaultMalformedUpstreamProblem("msgvault message returned an empty response")
	}
	out := buildMsgvaultMessageBody(detail)
	if detail.BodyHTML != "" {
		res, sanErr := sanitizer.Sanitize(ctx, detail.ID, detail.BodyHTML, gen)
		switch {
		case sanErr != nil:
			out.HTMLSanitizationFailed = true
		case h.sanitizerGeneration() != gen:
			out.HTMLSanitizationFailed = true
		default:
			out.BodyHTML = res.HTML
			if res.RemoteImageCount > 0 {
				out.RemoteImageCount = res.RemoteImageCount
				out.RemoteImageToken = res.Token
			}
		}
	}
	return &msgvaultMessageOutput{Body: out}, nil
}

func (h *msgvaultHandler) inline(ctx context.Context, in *msgvaultInlineInput) (*msgvaultRawImageOutput, error) {
	client, prob := h.requireConfigured()
	if prob != nil {
		return nil, prob
	}
	if in.CID == "" {
		return nil, problemBadRequest(
			CodeBadRequest,
			"cid query parameter is required",
			map[string]any{"reason": "missingCID"},
		)
	}
	contentType, body, err := client.InlineImage(ctx, in.ID, in.CID)
	if prob := msgvaultUpstreamProblem(err); prob != nil {
		return nil, prob
	}
	defer func() { _ = body.Close() }()
	baseType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if _, ok := msgvaultInlineSafeContentTypes[baseType]; !ok {
		return nil, newProblem(
			http.StatusUnsupportedMediaType,
			CodeBadRequest,
			"inline parts must be image/png, image/jpeg, image/gif, or image/webp",
			map[string]any{"reason": "inlineTypeNotAllowed"},
		)
	}
	buf, err := io.ReadAll(io.LimitReader(body, msgvaultInlineMaxBytes+1))
	if err != nil {
		return nil, newProblem(
			http.StatusBadGateway,
			CodeUpstreamError,
			"failed to read inline body from upstream",
			map[string]any{"reason": "inlineReadFailed"},
		)
	}
	if len(buf) > msgvaultInlineMaxBytes {
		return nil, newProblem(
			http.StatusBadGateway,
			CodeUpstreamError,
			fmt.Sprintf("inline body exceeded %d bytes", msgvaultInlineMaxBytes),
			map[string]any{"reason": "inlineTooLarge"},
		)
	}
	return &msgvaultRawImageOutput{
		ContentType:        baseType,
		ContentTypeOptions: "nosniff",
		CacheControl:       "private, max-age=31536000, immutable",
		ContentLength:      strconv.Itoa(len(buf)),
		Body:               buf,
	}, nil
}

func (h *msgvaultHandler) aggregates(ctx context.Context, in *msgvaultAggregatesInput) (*msgvaultAggregatesOutput, error) {
	client, prob := h.requireConfigured()
	if prob != nil {
		return nil, prob
	}
	if in.ViewType == "" {
		return nil, problemBadRequest(
			CodeBadRequest,
			"view_type is required",
			map[string]any{"reason": "missingViewType"},
		)
	}
	hideDeleted := in.HideDeleted != "false" && in.HideDeleted != "0"
	limit := max(in.Limit, 0)
	out, err := client.Aggregates(ctx, msgvault.AggregateParams{
		ViewType:    in.ViewType,
		SearchQuery: in.Q,
		HideDeleted: hideDeleted,
		Sort:        in.Sort,
		Direction:   in.Direction,
		Limit:       limit,
	})
	if prob := msgvaultUpstreamProblem(err); prob != nil {
		return nil, prob
	}
	if out == nil {
		return nil, msgvaultMalformedUpstreamProblem("msgvault aggregates returned an empty response")
	}
	return &msgvaultAggregatesOutput{Body: *out}, nil
}

func (h *msgvaultHandler) thread(ctx context.Context, in *msgvaultThreadInput) (*msgvaultThreadOutput, error) {
	client, prob := h.requireConfigured()
	if prob != nil {
		return nil, prob
	}
	messages, err := client.Thread(ctx, in.ConversationID, "date", "asc")
	if prob := msgvaultUpstreamProblem(err); prob != nil {
		return nil, prob
	}
	return &msgvaultThreadOutput{Body: msgvaultThreadBody{
		ConversationID: in.ConversationID,
		Messages:       messages,
	}}, nil
}

func (s *Server) configureMsgvault(_ context.Context, in *configureMsgvaultInput) (*msgvaultConfigureOutput, error) {
	if !isMsgvaultJSONMediaType(in.ContentType) {
		return nil, newProblem(
			http.StatusUnsupportedMediaType,
			CodeBadRequest,
			"configure requires Content-Type: application/json",
			nil,
		)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(in.RawBody, &raw); err != nil {
		return nil, problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "badRequest"})
	}
	if _, present := raw["api_key"]; present {
		return nil, problemBadRequest(
			CodeBadRequest,
			"api_key is not stored; set the env var named by api_key_env instead",
			map[string]any{"reason": "apiKeyUnsupported"},
		)
	}
	dec := json.NewDecoder(bytes.NewReader(in.RawBody))
	dec.DisallowUnknownFields()
	var req msgvaultConfigureRequest
	if err := dec.Decode(&req); err != nil {
		return nil, problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "badRequest"})
	}
	cleanURL, err := validateMsgvaultConfigureURL(req.URL)
	if err != nil {
		return nil, problemBadRequest(CodeBadRequest, err.Error(), map[string]any{"reason": "invalidURL"})
	}
	apiKeyEnv := strings.TrimSpace(req.APIKeyEnv)
	if !msgvaultEnvVarNameRegex.MatchString(apiKeyEnv) {
		return nil, problemBadRequest(
			CodeBadRequest,
			fmt.Sprintf("api_key_env must match /%s/", msgvaultEnvVarNameRegex.String()),
			map[string]any{"reason": "invalidEnvVarName"},
		)
	}
	if s.cfgPath == "" || s.cfg == nil {
		return nil, problemNotFound(CodeSettingsUnavailable, "settings not available", nil)
	}

	s.cfgMu.Lock()
	prev := cloneMsgvault(s.cfg.Msgvault)
	s.cfg.Msgvault = &config.Msgvault{URL: cleanURL, APIKeyEnv: apiKeyEnv}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.Msgvault = prev
		s.msgvault.applyConfig(s.cfg)
		s.cfgMu.Unlock()
		return nil, problemInternal("save config: " + err.Error())
	}
	s.msgvault.applyConfig(s.cfg)
	if s.runtime != nil {
		s.runtime.UpdateStripEnvVars(s.updateRuntimeStripEnvVarsLocked(s.cfg))
	}
	snapshot := s.msgvault.healthSnapshot()
	s.cfgMu.Unlock()

	return &msgvaultConfigureOutput{Body: snapshot.healthValue()}, nil
}

func buildMsgvaultMessageBody(detail *msgvault.MessageDetail) msgvaultMessageBody {
	return msgvaultMessageBody{
		ID:             detail.ID,
		ConversationID: detail.ConversationID,
		Subject:        detail.Subject,
		From:           detail.From,
		To:             detail.To,
		CC:             detail.CC,
		BCC:            detail.BCC,
		SentAt:         detail.SentAt,
		Snippet:        detail.Snippet,
		Labels:         detail.Labels,
		HasAttachments: detail.HasAttachments,
		SizeBytes:      detail.SizeBytes,
		DeletedAt:      detail.DeletedAt,
		Body:           detail.Body,
		Attachments:    detail.Attachments,
	}
}

func (h *msgvaultHandler) requireConfigured() (*msgvault.Client, huma.StatusError) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch h.state {
	case config.MsgvaultOK:
		if h.client != nil {
			return h.client, nil
		}
		return nil, newProblem(
			http.StatusServiceUnavailable,
			CodeServiceUnavailable,
			"msgvault client is not configured",
			map[string]any{"reason": "misconfigured"},
		)
	case config.MsgvaultMisconfigured:
		details := map[string]any{"reason": "misconfigured"}
		if h.configErr != nil {
			details["error"] = h.configErr.Error()
		}
		return nil, newProblem(
			http.StatusServiceUnavailable,
			CodeServiceUnavailable,
			"msgvault is misconfigured",
			details,
		)
	default:
		return nil, newProblem(
			http.StatusServiceUnavailable,
			CodeServiceUnavailable,
			"msgvault is not configured",
			map[string]any{"reason": "notConfigured"},
		)
	}
}

func (h *msgvaultHandler) requireConfiguredWithSanitizer() (*msgvault.Client, *msgvault.Sanitizer, uint64, huma.StatusError) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch h.state {
	case config.MsgvaultOK:
		if h.client != nil && h.sanitizer != nil {
			return h.client, h.sanitizer, h.sanitizer.Generation(), nil
		}
		return nil, nil, 0, newProblem(
			http.StatusServiceUnavailable,
			CodeServiceUnavailable,
			"msgvault client is not configured",
			map[string]any{"reason": "misconfigured"},
		)
	case config.MsgvaultMisconfigured:
		details := map[string]any{"reason": "misconfigured"}
		if h.configErr != nil {
			details["error"] = h.configErr.Error()
		}
		return nil, nil, 0, newProblem(
			http.StatusServiceUnavailable,
			CodeServiceUnavailable,
			"msgvault is misconfigured",
			details,
		)
	default:
		return nil, nil, 0, newProblem(
			http.StatusServiceUnavailable,
			CodeServiceUnavailable,
			"msgvault is not configured",
			map[string]any{"reason": "notConfigured"},
		)
	}
}

func (h *msgvaultHandler) sanitizerGeneration() uint64 {
	h.mu.Lock()
	sanitizer := h.sanitizer
	h.mu.Unlock()
	return sanitizer.Generation()
}

func msgvaultUpstreamProblem(err error) huma.StatusError {
	if err == nil {
		return nil
	}
	switch msgvault.Classify(err) {
	case "unauthorized":
		return newProblem(
			http.StatusServiceUnavailable,
			CodeServiceUnavailable,
			"msgvault rejected the configured API key",
			map[string]any{"reason": "unauthorized"},
		)
	case "not_found":
		return problemNotFound(
			CodeNotFound,
			err.Error(),
			map[string]any{"reason": "upstreamNotFound"},
		)
	case "rate_limited":
		return problemRateLimited("", "", nil)
	case "timeout":
		return newProblem(
			http.StatusGatewayTimeout,
			CodeUpstreamError,
			err.Error(),
			map[string]any{"reason": "upstreamTimeout"},
		)
	case "down":
		return newProblem(
			http.StatusBadGateway,
			CodeUpstreamError,
			err.Error(),
			map[string]any{"reason": "upstreamDown"},
		)
	case "malformed":
		return msgvaultMalformedUpstreamProblem(err.Error())
	default:
		return problemUpstream(err.Error(), "", "")
	}
}

func msgvaultMalformedUpstreamProblem(detail string) huma.StatusError {
	return newProblem(
		http.StatusBadGateway,
		CodeUpstreamError,
		detail,
		map[string]any{"reason": "upstreamMalformed"},
	)
}

func (h *msgvaultHandler) healthValue() msgvaultHealthBody {
	snapshot := h.healthSnapshot()
	switch snapshot.state {
	case config.MsgvaultAbsent, config.MsgvaultMisconfigured:
		return snapshot.healthValue()
	}

	h.capMu.Lock()
	defer h.capMu.Unlock()
	if h.capCache != nil && h.capCacheGen == snapshot.gen && time.Now().Before(h.capExpires) {
		return *h.capCache
	}

	body := snapshot.healthValue()
	cached := body
	h.capCache = &cached
	h.capCacheGen = snapshot.gen
	h.capExpires = time.Now().Add(msgvaultCapabilityCacheTTL)
	return body
}

func (h *msgvaultHandler) healthSnapshot() msgvaultHealthSnapshot {
	h.mu.Lock()
	snapshot := msgvaultHealthSnapshot{
		state:         h.state,
		configErr:     h.configErr,
		client:        h.client,
		configuredURL: h.configuredURL,
		configuredEnv: h.configuredEnv,
		gen:           h.gen,
	}
	h.mu.Unlock()
	return snapshot
}

func (s msgvaultHealthSnapshot) healthValue() msgvaultHealthBody {
	switch s.state {
	case config.MsgvaultAbsent:
		return msgvaultAbsentHealthBody()
	case config.MsgvaultMisconfigured:
		return msgvaultMisconfiguredHealthBody(s.configErr, s.configuredURL, s.configuredEnv)
	}

	probeCtx, cancel := context.WithTimeout(context.Background(), msgvaultCapabilityCacheTTL)
	defer cancel()
	var probeErr error
	if s.client == nil {
		probeErr = errors.New("msgvault client is not configured")
	} else {
		probeErr = s.client.Capabilities(probeCtx)
	}
	return msgvaultOKStateHealthBody(probeErr, s.configuredURL, s.configuredEnv)
}

func msgvaultAbsentHealthBody() msgvaultHealthBody {
	return msgvaultHealthBody{
		Configured: false,
		OK:         false,
		Modes:      []string{},
		Features:   msgvaultDefaultFeatures(),
	}
}

func msgvaultMisconfiguredHealthBody(configErr error, cfgURL, cfgEnv string) msgvaultHealthBody {
	body := msgvaultHealthBody{
		Configured: true,
		OK:         false,
		Status:     "misconfigured",
		Modes:      []string{},
		Features:   msgvaultDefaultFeatures(),
	}
	if configErr != nil {
		body.StatusDetail = configErr.Error()
	}
	echoMsgvaultCapabilityMetadata(&body, cfgURL, cfgEnv)
	return body
}

func msgvaultOKStateHealthBody(probeErr error, cfgURL, cfgEnv string) msgvaultHealthBody {
	body := msgvaultHealthBody{
		Configured: true,
		Modes:      []string{},
		Features:   msgvaultDefaultFeatures(),
	}
	if probeErr == nil {
		body.OK = true
		body.Status = "ok"
		body.Modes = []string{"fts"}
		body.Features = msgvaultOKFeatures()
	} else if msgvault.Classify(probeErr) == "unauthorized" {
		body.Status = "unauthorized"
	} else {
		body.Status = "down"
	}
	echoMsgvaultCapabilityMetadata(&body, cfgURL, cfgEnv)
	return body
}

func echoMsgvaultCapabilityMetadata(body *msgvaultHealthBody, cfgURL, cfgEnv string) {
	if isValidMsgvaultCapabilityURL(cfgURL) {
		body.URL = cfgURL
	}
	if msgvaultEnvVarNameRegex.MatchString(cfgEnv) {
		body.APIKeyEnv = cfgEnv
	}
}

func isValidMsgvaultCapabilityURL(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") &&
		u.Host != "" &&
		u.User == nil &&
		u.RawQuery == "" &&
		!u.ForceQuery &&
		u.Fragment == ""
}

var msgvaultEnvVarNameRegex = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func validateMsgvaultConfigureURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("url is required")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("url scheme must be http or https")
	}
	if u.Host == "" {
		return "", errors.New("url host is empty")
	}
	if u.Scheme == "http" && !config.IsLoopbackHostname(u.Hostname()) {
		return "", errors.New("http urls send the API key in cleartext; use https, or http with a loopback host (localhost/127.0.0.1)")
	}
	if u.User != nil {
		return "", errors.New("url must not include userinfo")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", errors.New("url must not include query string")
	}
	if u.Fragment != "" {
		return "", errors.New("url must not include fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func isMsgvaultJSONMediaType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json"
}

func documentMsgvaultConfigureJSONBody(api huma.API) {
	item, ok := api.OpenAPI().Paths["/msgvault/configure"]
	if !ok || item.Post == nil || item.Post.RequestBody == nil {
		return
	}
	item.Post.RequestBody.Content = map[string]*huma.MediaType{
		"application/json": {Schema: &huma.Schema{
			Type: "object",
			Properties: map[string]*huma.Schema{
				"url":         {Type: "string"},
				"api_key_env": {Type: "string"},
			},
			Required:             []string{"url", "api_key_env"},
			AdditionalProperties: false,
		}},
	}
}

func documentMsgvaultRawImageRoute(api huma.API, path string, requiredQuery string) {
	item, ok := api.OpenAPI().Paths[path]
	if !ok || item.Get == nil {
		return
	}
	response := item.Get.Responses["200"]
	if response == nil {
		response = &huma.Response{Description: "Image response"}
		item.Get.Responses["200"] = response
	}
	response.Content = map[string]*huma.MediaType{}
	for _, mediaType := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		response.Content[mediaType] = &huma.MediaType{
			Schema: &huma.Schema{Type: "string", Format: "binary"},
		}
	}
	for _, param := range item.Get.Parameters {
		if param.Name == requiredQuery && param.In == "query" {
			param.Required = true
		}
	}
}

func documentMsgvaultRequiredQuery(api huma.API, path string, name string) {
	item, ok := api.OpenAPI().Paths[path]
	if !ok || item.Get == nil {
		return
	}
	for _, param := range item.Get.Parameters {
		if param.Name == name && param.In == "query" {
			param.Required = true
		}
	}
}

func msgvaultDefaultFeatures() map[string]any {
	return map[string]any{
		"threads_endpoint":     false,
		"mutations":            false,
		"attachments_download": false,
		"sse_events":           false,
	}
}

func msgvaultOKFeatures() map[string]any {
	f := msgvaultDefaultFeatures()
	f["threads_endpoint"] = true
	return f
}
