package server

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

type providerProxy struct {
	client            providerplane.Client
	responseBodyLimit int64
}

func newProviderProxy(client providerplane.Client) *providerProxy {
	return &providerProxy{
		client: client, responseBodyLimit: providerProxyResponseBodyLimit,
	}
}

func (p *providerProxy) ServeHTTP(
	w http.ResponseWriter,
	r *http.Request,
	rule ProviderRouteRule,
) {
	if p == nil || p.client == nil {
		writeProblemResponse(w, httpapi.HubUnavailable(
			"provider data is unavailable because the federation hub cannot be reached",
		))
		return
	}
	response, err := p.client.Do(r.Context(), rule.PeerScope, r)
	if err != nil {
		if errors.Is(err, providerplane.ErrRequestBodyTooLarge) {
			writeProblemResponse(w, httpapi.NewProblem(
				http.StatusRequestEntityTooLarge,
				httpapi.CodePayloadTooLarge,
				"provider request body is too large",
				map[string]any{"maxBytes": 8 << 20},
			))
			return
		}
		if rule.PeerScope == federationauth.ScopeProviderWrite &&
			errors.Is(err, providerplane.ErrHubUnavailable) {
			writeProblemResponse(w, httpapi.MutationOutcomeUnknown(
				"The federation hub could not confirm whether the provider mutation was applied.",
				"", "",
			))
			return
		}
		writeProblemResponse(w, httpapi.HubUnavailable(
			"provider data is unavailable because the federation hub cannot be reached",
		))
		return
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, p.responseBodyLimit+1))
	if err != nil || int64(len(body)) > p.responseBodyLimit {
		if rule.PeerScope == federationauth.ScopeProviderWrite {
			writeProblemResponse(w, httpapi.MutationOutcomeUnknown(
				"The federation hub could not confirm whether the provider mutation was applied.",
				"", "",
			))
			return
		}
		writeProblemResponse(w, httpapi.NewProblem(
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
