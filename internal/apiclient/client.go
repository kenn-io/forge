package apiclient

import (
	"context"
	"net/http"
	"strings"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	"go.kenn.io/forge/internal/apiclient/generated"
)

type Client struct {
	HTTP      *generated.Client
	Transport *http.Client
}

func New(baseURL string) (*Client, error) {
	return NewWithHTTPClient(baseURL, http.DefaultClient)
}

func NewWithHTTPClient(baseURL string, httpClient *http.Client, options ...runtime.APIClientOption) (*Client, error) {
	options = append([]runtime.APIClientOption{runtime.WithHTTPClient(httpDoer{httpClient})}, options...)
	client, err := generated.NewDefaultClient(
		strings.TrimRight(baseURL, "/")+"/api/v1",
		options...,
	)
	if err != nil {
		return nil, err
	}
	return &Client{HTTP: client, Transport: httpClient}, nil
}

type httpDoer struct{ client *http.Client }

func (d httpDoer) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return d.client.Do(request.WithContext(ctx))
}
