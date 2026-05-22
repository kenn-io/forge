package apiclient

import (
	"net/http"
	"strings"

	"go.kenn.io/middleman/internal/apiclient/generated"
)

type Client struct {
	HTTP *generated.ClientWithResponses
}

func New(baseURL string) (*Client, error) {
	return NewWithHTTPClient(baseURL, http.DefaultClient)
}

func NewWithHTTPClient(baseURL string, httpClient *http.Client) (*Client, error) {
	client, err := generated.NewClientWithResponses(
		strings.TrimRight(baseURL, "/")+"/api/v1",
		generated.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, err
	}
	return &Client{HTTP: client}, nil
}
