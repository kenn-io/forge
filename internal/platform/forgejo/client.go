package forgejo

import (
	"context"
	"net/http"
	"strings"
	"time"

	forgejosdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	ghsync "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/platform/gitealike"
	"go.kenn.io/middleman/internal/ratelimit"
	"go.kenn.io/middleman/internal/tokenauth"
)

type ClientOption func(*clientOptions)

type clientOptions struct {
	baseURL           string
	foregroundTimeout time.Duration
	rateTracker       *ratelimit.RateTracker
	budget            *ghsync.SyncBudget
	skipVersionProbe  bool
}

type Client struct {
	host              string
	baseURL           string
	transport         *transport
	provider          *gitealike.Provider
	api               *forgejosdk.Client
	foregroundTimeout time.Duration
}

func WithBaseURLForTesting(baseURL string) ClientOption {
	return func(opts *clientOptions) {
		opts.baseURL = strings.TrimRight(baseURL, "/")
		opts.skipVersionProbe = true
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

	clientOptions := []forgejosdk.ClientOption{
		forgejosdk.SetUserAgent("middleman"),
	}
	if opts.skipVersionProbe {
		clientOptions = append(
			clientOptions,
			forgejosdk.SetForgejoVersion("13.0.0+gitea-1.26.0"),
		)
	}
	httpTransport := http.DefaultTransport
	if opts.rateTracker != nil {
		httpTransport = &rateTrackingTransport{
			base:        httpTransport,
			rateTracker: opts.rateTracker,
		}
	}
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
	authRT := tokenauth.AuthTransport{
		Source:              source,
		Base:                httpTransport,
		SetHeader:           tokenauth.TokenAuthHeader,
		RetryOnUnauthorized: true,
		AllowedOrigin:       opts.baseURL,
	}
	clientOptions = append(clientOptions, forgejosdk.SetHTTPClient(&http.Client{
		Timeout:   opts.foregroundTimeout,
		Transport: authRT,
	}))

	api, err := forgejosdk.NewClient(opts.baseURL, clientOptions...)
	if err != nil {
		return nil, err
	}
	transport := &transport{
		api:                api,
		budget:             opts.budget,
		mergeability:       mergeability,
		mergeRejections:    mergeRejections,
		requestContextLock: make(chan struct{}, 1),
	}
	return &Client{
		host:      host,
		baseURL:   opts.baseURL,
		api:       api,
		transport: transport,
		provider: gitealike.NewProvider(
			platform.KindForgejo,
			host,
			transport,
			gitealike.WithReadActions(),
			gitealike.WithMutations(),
		),
		foregroundTimeout: opts.foregroundTimeout,
	}, nil
}

func (c *Client) Platform() platform.Kind {
	return platform.KindForgejo
}

func (c *Client) Host() string {
	return c.host
}

func (c *Client) Capabilities() platform.Capabilities {
	caps := c.provider.Capabilities()
	caps.ReviewDraftMutation = true
	caps.ReadReviewThreads = true
	caps.NativeMultilineRanges = false
	caps.SupportedReviewActions = []platform.ReviewAction{
		platform.ReviewActionComment,
		platform.ReviewActionApprove,
		platform.ReviewActionRequestChanges,
	}
	return caps
}

type transport struct {
	api                *forgejosdk.Client
	budget             *ghsync.SyncBudget
	mergeability       *gitealike.MergeableCache
	mergeRejections    *gitealike.MergeRejectionCapture
	requestContextLock chan struct{}
}

func (t *transport) getRepositoryRaw(
	ctx context.Context, owner, repo string,
) (*forgejosdk.Repository, error) {
	t.spendSyncBudget(ctx)
	var repository *forgejosdk.Repository
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

func (t *transport) spendSyncBudget(ctx context.Context) {
	if t.budget != nil && ghsync.IsSyncBudgetContext(ctx) {
		t.budget.Spend(1)
	}
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
	}
	return resp, err
}
