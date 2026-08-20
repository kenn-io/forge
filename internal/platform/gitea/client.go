package gitea

import (
	"context"
	"net/http"
	"strings"
	"time"

	giteasdk "code.gitea.io/sdk/gitea"
	ghsync "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/platform/gitealike"
	"go.kenn.io/forge/internal/ratelimit"
	"go.kenn.io/forge/internal/tokenauth"
)

const minimumReviewThreadVersion = ">= 1.24.6"

type ClientOption func(*clientOptions)

type clientOptions struct {
	baseURL           string
	foregroundTimeout time.Duration
	rateTracker       *ratelimit.RateTracker
	budget            *ghsync.SyncBudget
	serverVersion     string
}

type provider = gitealike.Provider

type Client struct {
	host      string
	baseURL   string
	transport *transport
	*provider
	api               *giteasdk.Client
	foregroundTimeout time.Duration
	readReviewThreads bool
}

func WithBaseURLForTesting(baseURL string) ClientOption {
	return func(opts *clientOptions) {
		opts.baseURL = strings.TrimRight(baseURL, "/")
	}
}

func WithServerVersionForTesting(serverVersion string) ClientOption {
	return func(opts *clientOptions) {
		opts.serverVersion = serverVersion
	}
}

func WithForegroundTimeoutForTesting(timeout time.Duration) ClientOption {
	return func(opts *clientOptions) {
		opts.foregroundTimeout = timeout
	}
}

func WithRateTracker(rateTracker *ratelimit.RateTracker) ClientOption {
	return func(opts *clientOptions) {
		opts.rateTracker = rateTracker
	}
}

func WithSyncBudget(budget *ghsync.SyncBudget) ClientOption {
	return func(opts *clientOptions) {
		opts.budget = budget
	}
}

func NewClient(host string, source tokenauth.Source, options ...ClientOption) (*Client, error) {
	opts := clientOptions{
		baseURL:           "https://" + strings.TrimRight(host, "/"),
		foregroundTimeout: 20 * time.Second,
	}
	for _, option := range options {
		option(&opts)
	}

	clientOptions := []giteasdk.ClientOption{
		giteasdk.SetUserAgent("kenn-forge"),
	}
	if opts.serverVersion != "" {
		clientOptions = append(clientOptions, giteasdk.SetGiteaVersion(opts.serverVersion))
	}
	httpTransport := http.DefaultTransport
	if opts.rateTracker != nil {
		httpTransport = &rateTrackingTransport{
			base:        httpTransport,
			rateTracker: opts.rateTracker,
		}
	}
	httpTransport = ghsync.WrapSyncBudgetTransport(httpTransport, opts.budget)
	mergeability := gitealike.NewMergeableCache()
	httpTransport = &gitealike.MergeableCaptureTransport{
		Base:  httpTransport,
		Cache: mergeability,
	}
	mergeRejections := gitealike.NewMergeRejectionCapture()
	httpTransport = &gitealike.MergeRejectionCaptureTransport{
		Base:    httpTransport,
		Capture: mergeRejections,
	}
	httpTransport = &timelineLabelTransport{base: httpTransport}
	authRT := tokenauth.AuthTransport{
		Source:              source,
		Base:                httpTransport,
		SetHeader:           tokenauth.TokenAuthHeader,
		RetryOnUnauthorized: true,
		AllowedOrigin:       opts.baseURL,
	}
	apiHTTPClient := &http.Client{
		Timeout:   opts.foregroundTimeout,
		Transport: authRT,
	}
	clientOptions = append(clientOptions, giteasdk.SetHTTPClient(apiHTTPClient))

	api, err := giteasdk.NewClient(opts.baseURL, clientOptions...)
	if err != nil {
		return nil, err
	}
	readReviewThreads := api.CheckServerVersionConstraint(minimumReviewThreadVersion) == nil
	transport := &transport{
		api:                api,
		httpClient:         apiHTTPClient,
		baseURL:            opts.baseURL,
		mergeability:       mergeability,
		mergeRejections:    mergeRejections,
		requestContextLock: make(chan struct{}, 1),
	}
	return &Client{
		host:              host,
		baseURL:           opts.baseURL,
		api:               api,
		transport:         transport,
		provider:          gitealike.NewProvider(platform.KindGitea, host, transport, gitealike.WithReadActions(), gitealike.WithMutations()),
		foregroundTimeout: opts.foregroundTimeout,
		readReviewThreads: readReviewThreads,
	}, nil
}

func (c *Client) Platform() platform.Kind {
	return platform.KindGitea
}

func (c *Client) Host() string {
	return c.host
}

func (c *Client) Capabilities() platform.Capabilities {
	caps := c.provider.Capabilities()
	if c.readReviewThreads {
		caps.ReadReviewThreads = true
		caps.Archive.InlineReviewComments = true
	}
	return caps
}

func (c *Client) AuthenticatedUser(
	ctx context.Context,
	ref platform.RepoRef,
) (string, error) {
	return c.provider.AuthenticatedUser(ctx, ref)
}

type transport struct {
	api                *giteasdk.Client
	httpClient         *http.Client
	baseURL            string
	mergeability       *gitealike.MergeableCache
	mergeRejections    *gitealike.MergeRejectionCapture
	requestContextLock chan struct{}
}

func (t *transport) getRepositoryRaw(
	ctx context.Context, owner, repo string,
) (*giteasdk.Repository, error) {
	var repository *giteasdk.Repository
	err := t.withRequestContext(ctx, func() error {
		var err error
		repository, _, err = t.api.GetRepo(owner, repo)
		return err
	})
	return repository, err
}

func (t *transport) withRequestContext(ctx context.Context, request func() error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	select {
	case t.requestContextLock <- struct{}{}:
		defer func() { <-t.requestContextLock }()
	case <-ctx.Done():
		return ctx.Err()
	}

	t.api.SetContext(ctx)
	defer t.api.SetContext(context.Background())
	return request()
}

type rateTrackingTransport struct {
	base        http.RoundTripper
	rateTracker *ratelimit.RateTracker
}

func (t *rateTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if resp != nil && t.rateTracker != nil {
		t.rateTracker.RecordRequest()
		if rate, ok := gitealike.RateFromHeaders(resp.Header); ok {
			t.rateTracker.UpdateFromRate(rate)
		}
	}
	return resp, err
}
