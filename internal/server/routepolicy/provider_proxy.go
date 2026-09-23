package routepolicy

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
)

const providerProxyResponseBodyLimit = 32 << 20

type ProviderProxy struct {
	client            providerplane.Client
	ResponseBodyLimit int64
}

func NewProviderProxy(client providerplane.Client) *ProviderProxy {
	return &ProviderProxy{
		client: client, ResponseBodyLimit: providerProxyResponseBodyLimit,
	}
}

func (p *ProviderProxy) ServeHTTP(
	w http.ResponseWriter,
	r *http.Request,
	rule ProviderRouteRule,
) {
	if p == nil || p.client == nil {
		WriteProblemResponse(w, httpapi.HubUnavailable(
			"provider data is unavailable because the federation hub cannot be reached",
		))
		return
	}
	response, err := p.client.Do(r.Context(), rule.PeerScope, r)
	if err != nil {
		if errors.Is(err, providerplane.ErrRequestBodyTooLarge) {
			WriteProblemResponse(w, httpapi.NewProblem(
				http.StatusRequestEntityTooLarge,
				httpapi.CodePayloadTooLarge,
				"provider request body is too large",
				map[string]any{"maxBytes": 8 << 20},
			))
			return
		}
		if rule.PeerScope == federationauth.ScopeProviderWrite &&
			errors.Is(err, providerplane.ErrHubUnavailable) {
			WriteProblemResponse(w, httpapi.MutationOutcomeUnknown(
				"The federation hub could not confirm whether the provider mutation was applied.",
				"", "",
			))
			return
		}
		WriteProblemResponse(w, httpapi.HubUnavailable(
			"provider data is unavailable because the federation hub cannot be reached",
		))
		return
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, p.ResponseBodyLimit+1))
	if err != nil || int64(len(body)) > p.ResponseBodyLimit {
		if rule.PeerScope == federationauth.ScopeProviderWrite {
			WriteProblemResponse(w, httpapi.MutationOutcomeUnknown(
				"The federation hub could not confirm whether the provider mutation was applied.",
				"", "",
			))
			return
		}
		WriteProblemResponse(w, httpapi.NewProblem(
			http.StatusBadGateway,
			httpapi.CodeUpstreamError,
			"hub provider response exceeded the proxy limit",
			nil,
		))
		return
	}
	copyProviderResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
}

func copyProviderResponseHeaders(destination, source http.Header) {
	connectionHeaders := providerConnectionTokens(source)
	for key, values := range source {
		lower := strings.ToLower(key)
		if isProviderHopByHopHeader(lower) || connectionHeaders[lower] ||
			isUnsafeHubResponseHeader(lower) {
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func providerConnectionTokens(header http.Header) map[string]bool {
	tokens := make(map[string]bool)
	for _, value := range header.Values("Connection") {
		for token := range strings.SplitSeq(value, ",") {
			token = strings.ToLower(strings.TrimSpace(token))
			if token != "" {
				tokens[token] = true
			}
		}
	}
	return tokens
}

func isProviderHopByHopHeader(lower string) bool {
	switch lower {
	case "connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func isUnsafeHubResponseHeader(lower string) bool {
	switch lower {
	case "set-cookie", "location", "clear-site-data", "www-authenticate":
		return true
	default:
		return false
	}
}
