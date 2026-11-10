package roborevapi

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/httpapi"
)

// roborevProxy returns an HTTP handler that reverse-proxies requests
// to the roborev daemon. It strips the /api/roborev prefix before
// forwarding and streams SSE/NDJSON responses without buffering.
func roborevProxy(target string, transport http.RoundTripper) http.Handler {
	targetURL, err := url.Parse(target)
	if err != nil {
		// Static config value; if it's invalid, fail visibly.
		panic("roborev: invalid target URL: " + err.Error())
	}

	proxy := &httputil.ReverseProxy{
		// Flush immediately for streaming responses (SSE, NDJSON).
		FlushInterval: -1,
		Transport:     transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(targetURL)
			r.Out.Header.Del("Cookie")
		},
		ErrorHandler: func(
			w http.ResponseWriter, _ *http.Request, _ error,
		) {
			authapi.WriteJSON(w, http.StatusBadGateway, map[string]string{
				"error": "roborev daemon is not reachable",
			})
		},
	}
	// StripPrefix removes /api/roborev before the proxy
	// sees the request, handling both Path and RawPath.
	return http.StripPrefix("/api/roborev", proxy)
}

type RoborevStatusResponse struct {
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Endpoint  string `json:"endpoint"`
}

type roborevStatusOutput = httpapi.BodyOutput[RoborevStatusResponse]

// getRoborevStatus probes the roborev daemon and reports whether
// it is reachable and what version it advertises.
func (s *Handlers) GetRoborevStatus(
	ctx context.Context, _ *struct{},
) (*roborevStatusOutput, error) {
	endpoint := s.Endpoint
	// Sanitize: strip credentials and path, expose
	// only scheme + host for the UI error message.
	sanitized := "(configured endpoint)"
	if u, err := url.Parse(endpoint); err == nil {
		u.User = nil
		u.Path = ""
		u.RawQuery = ""
		u.Fragment = ""
		sanitized = u.String()
	}
	resp := RoborevStatusResponse{Endpoint: sanitized}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	statusURL := strings.TrimRight(endpoint, "/") + "/api/status"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return &roborevStatusOutput{Body: resp}, nil
	}
	statusResp, err := s.Client.Do(req)
	if err != nil {
		return &roborevStatusOutput{Body: resp}, nil
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode < 200 || statusResp.StatusCode >= 300 {
		return &roborevStatusOutput{Body: resp}, nil
	}

	var body struct {
		Version string `json:"version"`
	}
	if err := json.UnmarshalRead(statusResp.Body, &body); err != nil {
		return &roborevStatusOutput{Body: resp}, nil
	}
	resp.Available = true
	resp.Version = body.Version
	return &roborevStatusOutput{Body: resp}, nil
}
