package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	katagenerated "go.kenn.io/kata/pkg/client/generated"

	"go.kenn.io/middleman/internal/kata"
)

const (
	// Generated responses are decoded from a complete byte slice by the
	// generated runtime. Keep a generous default ceiling while still bounding
	// the memory a configured daemon can make Middleman retain per request.
	kataGeneratedResponseMaxBytes = int64(32 << 20)

	// Authority and graph endpoints legitimately return substantially more
	// data than detail and paginated endpoints. This is intentionally far above
	// the former 8 MiB raw-read limit so complete federated authorities are not
	// mistaken for oversized responses.
	kataGeneratedAuthorityResponseMaxBytes = int64(128 << 20)
)

type kataAPIClient interface {
	InstanceWithResponse(
		ctx context.Context,
		reqEditors ...runtime.RequestEditorFn,
	) (*katagenerated.InstanceResp, error)
	ListAllIssuesWithResponse(
		ctx context.Context,
		options *katagenerated.ListAllIssuesRequestOptions,
		reqEditors ...runtime.RequestEditorFn,
	) (*katagenerated.ListAllIssuesResp, error)
	ListProjectsWithResponse(
		ctx context.Context,
		options *katagenerated.ListProjectsRequestOptions,
		reqEditors ...runtime.RequestEditorFn,
	) (*katagenerated.ListProjectsResp, error)
	PollEventsWithResponse(
		ctx context.Context,
		options *katagenerated.PollEventsRequestOptions,
		reqEditors ...runtime.RequestEditorFn,
	) (*katagenerated.PollEventsResp, error)
	PollProjectEventsWithResponse(
		ctx context.Context,
		options *katagenerated.PollProjectEventsRequestOptions,
		reqEditors ...runtime.RequestEditorFn,
	) (*katagenerated.PollProjectEventsResp, error)
	ReadyIssuesWithResponse(
		ctx context.Context,
		options *katagenerated.ReadyIssuesRequestOptions,
		reqEditors ...runtime.RequestEditorFn,
	) (*katagenerated.ReadyIssuesResp, error)
	ReadyIssuesGlobalWithResponse(
		ctx context.Context,
		options *katagenerated.ReadyIssuesGlobalRequestOptions,
		reqEditors ...runtime.RequestEditorFn,
	) (*katagenerated.ReadyIssuesGlobalResp, error)
	ReachableIssueGraphWithResponse(
		ctx context.Context,
		options *katagenerated.ReachableIssueGraphRequestOptions,
		reqEditors ...runtime.RequestEditorFn,
	) (*katagenerated.ReachableIssueGraphResp, error)
	ShowIssueByUIDWithResponse(
		ctx context.Context,
		options *katagenerated.ShowIssueByUIDRequestOptions,
		reqEditors ...runtime.RequestEditorFn,
	) (*katagenerated.ShowIssueByUIDResp, error)
	StreamEventsRaw(
		ctx context.Context,
		options *katagenerated.StreamEventsRequestOptions,
		reqEditors ...runtime.RequestEditorFn,
	) (*http.Response, error)
}

func newKataAPIClient(ctx context.Context, daemon kata.Daemon) (kataAPIClient, error) {
	httpClient, baseURL, err := kataDaemonHTTPClient(daemon)
	if err != nil {
		return nil, err
	}
	options := []runtime.APIClientOption{
		runtime.WithHTTPClient(kataGeneratedHTTPDoer{client: httpClient}),
	}
	if token := kataDaemonForwardToken(daemon); token != "" {
		options = append(options, runtime.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		}))
	}
	apiClient, err := runtime.NewAPIClient(baseURL, options...)
	if err != nil {
		return nil, err
	}
	return &kataGeneratedClient{
		Client:     katagenerated.NewClient(apiClient),
		apiClient:  apiClient,
		httpClient: httpClient,
	}, nil
}

type kataGeneratedClient struct {
	*katagenerated.Client

	apiClient  runtime.APIClient
	httpClient *http.Client
}

func (c *kataGeneratedClient) StreamEventsRaw(
	ctx context.Context,
	options *katagenerated.StreamEventsRequestOptions,
	reqEditors ...runtime.RequestEditorFn,
) (*http.Response, error) {
	if options == nil {
		options = &katagenerated.StreamEventsRequestOptions{}
	}
	req, err := c.apiClient.CreateRequest(ctx, runtime.RequestOptionsParameters{
		RequestURL: c.apiClient.GetBaseURL() + "/api/v1/events/stream",
		Method:     http.MethodGet,
		Options:    options,
	}, reqEditors...)
	if err != nil {
		return nil, fmt.Errorf("create Kata event stream request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	response, err := c.httpClient.Do(req.WithContext(ctx)) //nolint:gosec // generated client builds the URL from the selected daemon base
	if err != nil {
		return nil, fmt.Errorf("open Kata event stream: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, runtime.NewClientAPIError(
			fmt.Errorf("kata event stream returned status %d", response.StatusCode),
			runtime.WithStatusCode(response.StatusCode),
		)
	}
	return response, nil
}

type kataGeneratedHTTPDoer struct {
	client          *http.Client
	limitForRequest func(*http.Request) int64
}

func (d kataGeneratedHTTPDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	response, err := d.client.Do(req.WithContext(ctx)) //nolint:gosec // generated client builds the URL from the selected daemon base
	if err != nil {
		return nil, err
	}
	limitForRequest := d.limitForRequest
	if limitForRequest == nil {
		limitForRequest = func(request *http.Request) int64 {
			return kataGeneratedResponseLimit(request.URL.Path)
		}
	}
	limit := limitForRequest(req)
	response.Body = &kataLimitedDaemonResponseBody{
		body:      response.Body,
		remaining: limit,
		limit:     limit,
		path:      req.URL.Path,
	}
	return response, nil
}

func kataGeneratedResponseLimit(path string) int64 {
	if path == "/api/v1/issues" || path == "/api/v1/ready" ||
		strings.HasSuffix(path, "/ready") || strings.HasSuffix(path, "/graph") {
		return kataGeneratedAuthorityResponseMaxBytes
	}
	return kataGeneratedResponseMaxBytes
}

type kataDaemonResponseTooLargeError struct {
	Path  string
	Limit int64
}

func (e *kataDaemonResponseTooLargeError) Error() string {
	return fmt.Sprintf("Kata daemon response for %s exceeded %d bytes", e.Path, e.Limit)
}

type kataLimitedDaemonResponseBody struct {
	body      io.ReadCloser
	remaining int64
	limit     int64
	path      string
	tooLarge  bool
}

func (b *kataLimitedDaemonResponseBody) Read(buffer []byte) (int, error) {
	if b.tooLarge {
		return 0, &kataDaemonResponseTooLargeError{Path: b.path, Limit: b.limit}
	}
	if len(buffer) == 0 {
		return b.body.Read(buffer)
	}
	if int64(len(buffer)) > b.remaining+1 {
		buffer = buffer[:b.remaining+1]
	}
	read, err := b.body.Read(buffer)
	if int64(read) <= b.remaining {
		b.remaining -= int64(read)
		return read, err
	}
	read = int(b.remaining)
	b.remaining = 0
	b.tooLarge = true
	return read, &kataDaemonResponseTooLargeError{Path: b.path, Limit: b.limit}
}

func (b *kataLimitedDaemonResponseBody) Close() error {
	return b.body.Close()
}
