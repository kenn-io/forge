package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/config"
)

// roborevProxy returns an HTTP handler that reverse-proxies requests
// to the roborev daemon. It strips the /api/roborev prefix before
// forwarding and streams SSE/NDJSON responses without buffering.
func roborevProxy(target string) http.Handler {
	targetURL, err := url.Parse(target)
	if err != nil {
		// Static config value; if it's invalid, fail visibly.
		panic("roborev: invalid target URL: " + err.Error())
	}

	proxy := &httputil.ReverseProxy{
		// Flush immediately for streaming responses (SSE, NDJSON).
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(targetURL)
		},
		ErrorHandler: func(
			w http.ResponseWriter, _ *http.Request, _ error,
		) {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "roborev daemon is not reachable",
			})
		},
	}
	// StripPrefix removes /api/roborev before the proxy
	// sees the request, handling both Path and RawPath.
	return http.StripPrefix("/api/roborev", proxy)
}

type roborevStatusResponse struct {
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Endpoint  string `json:"endpoint"`
}

type roborevStatusOutput = bodyOutput[roborevStatusResponse]

// getRoborevStatus probes the roborev daemon and reports whether
// it is reachable and what version it advertises.
func (s *Server) getRoborevStatus(
	_ context.Context, _ *struct{},
) (*roborevStatusOutput, error) {
	cfg := s.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	endpoint := cfg.RoborevEndpoint()
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
	resp := roborevStatusResponse{Endpoint: sanitized}

	client := &http.Client{Timeout: 2 * time.Second}
	statusURL := strings.TrimRight(endpoint, "/") + "/api/status"

	r, err := client.Get(statusURL)
	if err != nil {
		return &roborevStatusOutput{Body: resp}, nil
	}
	defer r.Body.Close()

	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return &roborevStatusOutput{Body: resp}, nil
	}
	resp.Available = true
	resp.Version = body.Version
	return &roborevStatusOutput{Body: resp}, nil
}
