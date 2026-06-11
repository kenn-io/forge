package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/middleman/internal/kata"
)

const (
	kataDaemonHealthTTL     = 5 * time.Second
	kataDaemonProbeTimeout  = 2 * time.Second
	kataDaemonCacheKeyDelim = "\x00"
)

type kataDaemonResponse struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Default bool   `json:"default"`
	Auth    string `json:"auth"`
	Health  string `json:"health"`
	Hint    string `json:"hint,omitempty"`
}

type kataDaemonRosterResponse struct {
	Daemons []kataDaemonResponse `json:"daemons"`
	Source  string               `json:"source,omitempty"`
}

type listKataDaemonsOutput = bodyOutput[kataDaemonRosterResponse]

type kataDaemonHealthCacheEntry struct {
	state   string
	expires time.Time
}

type kataDaemonInflightProbe struct {
	wg     sync.WaitGroup
	result string
}

func (s *Server) registerKataAPI(api huma.API) {
	huma.Get(api, "/kata/daemons", s.listKataDaemons,
		documentOperation("list-kata-daemons", "List Kata daemons", "Kata"))
	s.registerKataProxyAPI(api)
}

func (s *Server) listKataDaemons(context.Context, *struct{}) (*listKataDaemonsOutput, error) {
	catalog, err := kata.LoadCatalog()
	if err != nil {
		return nil, problemBadRequest(CodeBadRequest, err.Error(), nil)
	}

	resolved := make([]kata.Daemon, len(catalog.Daemons))
	for i, configured := range catalog.Daemons {
		d, err := kata.ResolveDaemon(configured)
		if err != nil {
			return nil, problemBadRequest(CodeBadRequest, err.Error(), nil)
		}
		if d.Local && d.URL == "" {
			d.URL = kata.DiscoverLocalDaemonURL()
		}
		resolved[i] = d
	}

	health := make([]string, len(resolved))
	var wg sync.WaitGroup
	wg.Add(len(resolved))
	for i, configured := range catalog.Daemons {
		go func() {
			defer wg.Done()
			health[i] = s.kataDaemonHealth(configured.ID, resolved[i])
		}()
	}
	wg.Wait()

	out := kataDaemonRosterResponse{
		Daemons: []kataDaemonResponse{},
		Source:  catalog.Source,
	}
	defaultID := effectiveKataDefaultID(catalog.Daemons)
	for i, configured := range catalog.Daemons {
		d := resolved[i]
		auth := "none"
		if d.Token != "" {
			auth = "token"
		}
		hint := ""
		if d.Local && d.URL == "" {
			hint = "local daemon not running; run `kata daemon start`"
		}

		out.Daemons = append(out.Daemons, kataDaemonResponse{
			ID:      configured.ID,
			URL:     kata.RedactURL(d.URL),
			Default: configured.ID == defaultID,
			Auth:    auth,
			Health:  health[i],
			Hint:    hint,
		})
	}

	return &listKataDaemonsOutput{Body: out}, nil
}

func effectiveKataDefaultID(daemons []kata.Daemon) string {
	for _, d := range daemons {
		if d.Default {
			return d.ID
		}
	}
	if len(daemons) > 0 {
		return daemons[0].ID
	}
	return ""
}

func (s *Server) kataDaemonHealth(id string, d kata.Daemon) string {
	if d.URL == "" {
		return "down"
	}
	cacheKey := id + kataDaemonCacheKeyDelim + d.URL

	s.kataHealthMu.Lock()
	if s.kataHealthCache == nil {
		s.kataHealthCache = map[string]kataDaemonHealthCacheEntry{}
	}
	if s.kataHealthInFlight == nil {
		s.kataHealthInFlight = map[string]*kataDaemonInflightProbe{}
	}
	if c, ok := s.kataHealthCache[cacheKey]; ok && time.Now().Before(c.expires) {
		state := c.state
		s.kataHealthMu.Unlock()
		return state
	}
	if fp, ok := s.kataHealthInFlight[cacheKey]; ok {
		s.kataHealthMu.Unlock()
		fp.wg.Wait()
		return fp.result
	}
	fp := &kataDaemonInflightProbe{}
	fp.wg.Add(1)
	s.kataHealthInFlight[cacheKey] = fp
	s.kataHealthMu.Unlock()

	state := probeKataDaemon(id, d)

	s.kataHealthMu.Lock()
	s.kataHealthCache[cacheKey] = kataDaemonHealthCacheEntry{
		state:   state,
		expires: time.Now().Add(kataDaemonHealthTTL),
	}
	delete(s.kataHealthInFlight, cacheKey)
	s.kataHealthMu.Unlock()

	fp.result = state
	fp.wg.Done()
	return state
}

func probeKataDaemon(id string, d kata.Daemon) string {
	probeURL, transport, err := kataDaemonProbeTarget(d.URL)
	if err != nil {
		slog.Warn("kata daemon health probe target invalid",
			"daemon", id, "target", kata.RedactURL(d.URL), "err", err)
		return "down"
	}

	ctx, cancel := context.WithTimeout(context.Background(), kataDaemonProbeTimeout)
	defer cancel()
	target, err := url.JoinPath(probeURL.String(), "api/v1/instance")
	if err != nil {
		return "down"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "down"
	}
	if d.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.Token)
	}

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		cause := err
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			cause = urlErr.Err
		}
		slog.Warn("kata daemon health probe failed",
			"daemon", id, "target", kata.RedactURL(target), "err", cause)
		return "down"
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "connected"
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "auth_required"
	default:
		return "down"
	}
}

func kataDaemonProbeTarget(target string) (*url.URL, http.RoundTripper, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, nil, err
	}
	switch parsed.Scheme {
	case "http", "https":
		if parsed.Host == "" {
			return nil, nil, errors.New("daemon url must include a host")
		}
		return &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}, nil, nil
	case "unix":
		if strings.TrimSpace(parsed.Path) == "" {
			return nil, nil, errors.New("daemon url must include a socket path")
		}
		socketPath := parsed.Path
		return &url.URL{Scheme: "http", Host: "kata.invalid"}, &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		}, nil
	default:
		return nil, nil, errors.New("daemon url scheme must be http, https, or unix")
	}
}
