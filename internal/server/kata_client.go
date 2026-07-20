package server

import (
	"context"
	"net/http"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	katagenerated "go.kenn.io/kata/pkg/client/generated"

	"go.kenn.io/middleman/internal/kata"
)

type kataAPIClient interface {
	InstanceWithResponse(
		ctx context.Context,
		reqEditors ...runtime.RequestEditorFn,
	) (*katagenerated.InstanceResp, error)
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
	return katagenerated.NewClient(katagenerated.NewPathEscapingAPIClient(apiClient)), nil
}

type kataGeneratedHTTPDoer struct {
	client *http.Client
}

func (d kataGeneratedHTTPDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return d.client.Do(req.WithContext(ctx)) //nolint:gosec // generated client builds the URL from the selected daemon base
}
