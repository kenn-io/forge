// Package bitbucketdc implements the Bitbucket Data Center REST API.
package bitbucketdc

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

type Client struct {
	host    string
	baseURL string
	http    *http.Client
	rate    platform.RateObserver
}

func NewClient(host string, source platform.CredentialSource, transport http.RoundTripper, rate platform.RateObserver) (*Client, error) {
	ref := platform.RepoRef{Platform: platform.KindBitbucket, Host: host, Owner: "project", Name: "repo"}
	if err := platform.ValidateCanonicalRepoRef(ref); err != nil {
		return nil, err
	}
	if host == platform.DefaultBitbucketHost || transport == nil {
		return nil, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "platform_host"}
	}
	base := "https://" + host
	return &Client{host: host, baseURL: base, rate: rate, http: &http.Client{Timeout: 30 * time.Second, Transport: platform.AuthTransport{
		Source: source, Base: transport, AllowedOrigin: base, SetHeader: func(r *http.Request, token string) {
			if user, password, ok := strings.Cut(token, ":"); ok {
				r.SetBasicAuth(user, password)
			} else {
				platform.BearerAuthHeader(r, token)
			}
		},
	}}}, nil
}
func (*Client) Platform() platform.Kind { return platform.KindBitbucket }
func (c *Client) Host() string          { return c.host }
func (*Client) Capabilities() platform.Capabilities {
	return platform.Capabilities{ReadRepositories: true, ReadMergeRequests: true, ReadComments: true, ReadCI: true, CommentMutation: true, StateMutation: true, MergeMutation: true, ReviewMutation: true, ThreadReply: true, ReadReviewThreads: true, SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment, platform.ReviewActionApprove}}
}

func (c *Client) repoPath(ref platform.RepoRef) (string, error) {
	if err := platform.ValidateCanonicalRepoRef(ref); err != nil {
		return "", err
	}
	if ref.Platform != c.Platform() || ref.Host != c.host {
		return "", &platform.Error{Code: platform.ErrCodeInvalidRepoRef}
	}
	return "/rest/api/latest/projects/" + url.PathEscape(ref.Owner) + "/repos/" + url.PathEscape(ref.Name), nil
}

func (c *Client) pullPath(ref platform.RepoRef, n int) (string, error) {
	path, err := c.repoPath(ref)
	if err != nil {
		return "", err
	}
	if n < 1 {
		return "", &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "number"}
	}
	return path + "/pull-requests/" + strconv.Itoa(n), nil
}

func request[T any](ctx context.Context, c *Client, method, path string, body any) (T, error) {
	var result T
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return result, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if c.rate != nil {
		c.rate.RecordRequest()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var code platform.PlatformErrorCode
		switch resp.StatusCode {
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
			return result, errors.New("bitbucket data center: " + resp.Status)
		}
		return result, &platform.Error{Code: code, Provider: c.Platform(), PlatformHost: c.host, Err: errors.New(resp.Status)}
	}
	if resp.StatusCode == http.StatusNoContent {
		return result, nil
	}
	err = json.UnmarshalRead(resp.Body, &result)
	return result, err
}

type page[T any] struct {
	Values []T   `json:"values"`
	Last   *bool `json:"isLastPage"`
	Next   *int  `json:"nextPageStart"`
}

func pages[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	rows := []T{}
	start := 0
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	for range 100 {
		p, err := request[page[T]](ctx, c, http.MethodGet, path+separator+"limit=100&start="+strconv.Itoa(start), nil)
		if err != nil {
			return nil, err
		}
		if p.Last == nil {
			return nil, platform.ProviderContract(c.Platform(), c.host, "isLastPage", errors.New("missing page completion flag"))
		}
		rows = append(rows, p.Values...)
		if *p.Last {
			return rows, nil
		}
		if p.Next == nil || *p.Next <= start {
			return nil, platform.ProviderContract(c.Platform(), c.host, "nextPageStart", errors.New("invalid next page cursor"))
		}
		start = *p.Next
	}
	return nil, &platform.Error{Code: platform.ErrCodePageLimit, Provider: c.Platform()}
}
