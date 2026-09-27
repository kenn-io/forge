// Package bitbucket implements Bitbucket Cloud's REST v2 API.
package bitbucket

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	bitbucket "github.com/ktrysmt/go-bitbucket"
	"go.kenn.io/forge/platform"
)

const apiURL = "https://api.bitbucket.org/2.0"

type Client struct {
	http                 *http.Client
	rate                 platform.RateObserver
	source               platform.CredentialSource
	permissionMu         sync.Mutex
	permissionCredential [32]byte
	permissionWorkspaces map[string]permissionSnapshot
}

func NewClient(host string, source platform.CredentialSource, transport http.RoundTripper, rate platform.RateObserver) (*Client, error) {
	if host != platform.DefaultBitbucketHost {
		return nil, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "platform_host", Err: errors.New("bitbucket Cloud requires bitbucket.org")}
	}
	if transport == nil {
		return nil, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "transport"}
	}
	c := &Client{rate: rate, source: source}
	c.http = &http.Client{Timeout: 30 * time.Second, Transport: platform.AuthTransport{
		Source: source, Base: transport, AllowedOrigin: apiURL,
		SetHeader: func(req *http.Request, token string) {
			// API tokens use account email:token; OAuth and repository access
			// tokens use a bearer token. Credential discovery stays with callers.
			if email, secret, ok := strings.Cut(token, ":"); ok {
				req.SetBasicAuth(email, secret)
			} else {
				platform.BearerAuthHeader(req, token)
			}
		},
	}}
	return c, nil
}

func (*Client) Platform() platform.Kind { return platform.KindBitbucket }
func (*Client) Host() string            { return platform.DefaultBitbucketHost }

func (*Client) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadRepositories: true, ReadMergeRequests: true, ReadIssues: true,
		ReadComments: true, ReadCI: true, ReadAuthenticatedUser: true,
		CommentMutation: true, MergeMutation: true, ReviewMutation: true,
		IssueMutation: true, ThreadReply: true, ReadReviewThreads: true,
		ReviewerMutation: true, ReviewThreadResolution: true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment, platform.ReviewActionApprove, platform.ReviewActionRequestChanges},
	}
}

// The SDK does not propagate context through all methods. Each operation gets
// its own client whose transport binds every request to the caller's context.
// Auto paging is disabled: v0.10.0 ignores later-page JSON errors and does not
// close later-page bodies. collect owns complete-or-error traversal instead.
func (c *Client) sdk(ctx context.Context) (*bitbucket.Client, error) {
	api, err := bitbucket.NewBasicAuthWithBaseUrlStr("", "", apiURL)
	if err != nil {
		return nil, err
	}
	api.DisableAutoPaging = true
	api.Pagelen = 100
	api.HttpClient = &http.Client{Timeout: c.http.Timeout, Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := c.http.Transport.RoundTrip(req.Clone(ctx))
		if resp != nil && c.rate != nil {
			c.observeRate(resp.StatusCode)
		}
		return resp, err
	})}
	return api, nil
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	response, ok := errors.AsType[*bitbucket.UnexpectedResponseStatusError](err)
	if !ok {
		return err
	}
	var code platform.PlatformErrorCode
	switch response.StatusCode {
	case 400:
		code = platform.ErrCodeInvalidArgument
	case 401, 403:
		code = platform.ErrCodePermissionDenied
	case 404:
		code = platform.ErrCodeNotFound
	case 409:
		code = platform.ErrCodeConflict
	case 429:
		code = platform.ErrCodeRateLimited
	default:
		return err
	}
	return &platform.Error{Code: code, Provider: platform.KindBitbucket, PlatformHost: platform.DefaultBitbucketHost, Err: err}
}

func decode[T any](value any, err error) (T, error) {
	var out T
	if err != nil {
		return out, classify(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

func request[T any](ctx context.Context, c *Client, method, target string, body any) (T, error) {
	var out T
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return out, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(data))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if c.rate != nil {
		c.observeRate(resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, classify(&bitbucket.UnexpectedResponseStatusError{Status: resp.Status, StatusCode: resp.StatusCode})
	}
	if resp.StatusCode == http.StatusNoContent {
		return out, nil
	}
	err = json.UnmarshalRead(resp.Body, &out)
	return out, err
}

type page[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

func collect[T any](ctx context.Context, c *Client, first page[T]) ([]T, error) {
	items := first.Values
	next := first.Next
	for n := 1; next != ""; n++ {
		if n >= 100 {
			return nil, &platform.Error{Code: platform.ErrCodePageLimit, Provider: platform.KindBitbucket}
		}
		p, err := request[page[T]](ctx, c, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		items = append(items, p.Values...)
		next = p.Next
	}
	return items, nil
}

func repoParts(ref platform.RepoRef) (string, string, error) {
	if ref.Platform != platform.KindBitbucket || ref.Host != platform.DefaultBitbucketHost || ref.Owner == "" || ref.Name == "" {
		return "", "", &platform.Error{Code: platform.ErrCodeInvalidRepoRef, Provider: platform.KindBitbucket}
	}
	name := ref.Name
	if ref.PlatformExternalID != "" {
		name = ref.PlatformExternalID
	}
	if strings.ContainsAny(ref.Owner+name, "/?#%") || ref.Owner == "." || ref.Owner == ".." || name == "." || name == ".." {
		return "", "", &platform.Error{Code: platform.ErrCodeInvalidRepoRef, Provider: platform.KindBitbucket}
	}
	return ref.Owner, name, nil
}

func repoURL(ref platform.RepoRef) (string, error) {
	owner, name, err := repoParts(ref)
	return apiURL + "/repositories/" + url.PathEscape(owner) + "/" + url.PathEscape(name), err
}

func pullOptions(ref platform.RepoRef, number int) (*bitbucket.PullRequestsOptions, error) {
	owner, name, err := repoParts(ref)
	return &bitbucket.PullRequestsOptions{Owner: owner, RepoSlug: name, ID: strconv.Itoa(number)}, err
}

func issueOptions(ref platform.RepoRef, number int) (*bitbucket.IssuesOptions, error) {
	owner, name, err := repoParts(ref)
	return &bitbucket.IssuesOptions{Owner: owner, RepoSlug: name, ID: strconv.Itoa(number)}, err
}

func missing(field string) error {
	return platform.ProviderContract(platform.KindBitbucket, platform.DefaultBitbucketHost, field, fmt.Errorf("bitbucket omitted %s", field))
}

// A 429 proves exhaustion, but neither product promises a common reset header.
// The observer's unknown-reset policy supplies the bounded pause. A successful
// request releases that observation without inventing a remaining quota.
func (c *Client) observeRate(status int) {
	c.rate.RecordRequest()
	if status == http.StatusTooManyRequests {
		c.rate.UpdateFromRate(platform.Rate{Remaining: 0, Limit: -1})
	} else if status >= 200 && status < 300 {
		c.rate.UpdateFromRate(platform.Rate{Remaining: -1, Limit: -1})
	}
}
