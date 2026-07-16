package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/kata"
)

const (
	kataDaemonHeaderName          = "X-Middleman-Kata-Daemon"
	kataProxyPrefix               = "/api/v1/kata/proxy"
	kataDaemonProxyRequestTimeout = 30 * time.Second
)

type kataProxyCacheKey struct {
	id    string
	url   string
	token string
	local bool
}

type kataProxyCacheEntry struct {
	handler   http.Handler
	closeIdle func()
}

type kataProxyDeadlineHandler struct {
	proxy          http.Handler
	requestTimeout time.Duration
}

func (h *kataProxyDeadlineHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/events/stream" {
		h.proxy.ServeHTTP(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.requestTimeout)
	defer cancel()
	h.proxy.ServeHTTP(w, r.WithContext(ctx))
}

func (s *Server) kataProxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", kataDaemonHeaderName)

		selected, ok := s.selectKataProxyDaemon(w, r)
		if !ok {
			return
		}
		entry, err := s.kataProxyForDaemon(selected)
		if err != nil {
			slog.Warn("kata proxy target invalid",
				"daemon", selected.ID, "target", kata.RedactURL(selected.URL), "err", err)
			writeProblemResponse(w, newProblem(
				http.StatusBadRequest,
				CodeBadRequest,
				"invalid Kata daemon target",
				map[string]any{"daemon": selected.ID},
			))
			return
		}

		http.StripPrefix(kataProxyPrefix, entry.handler).ServeHTTP(w, r)
	})
}

func (s *Server) kataProxyForDaemon(d kata.Daemon) (kataProxyCacheEntry, error) {
	key := kataProxyCacheKey{id: d.ID, url: d.URL, token: kataDaemonForwardToken(d), local: d.Local}
	s.kataProxyMu.Lock()
	if entry, ok := s.kataProxyCache[key]; ok {
		s.kataProxyMu.Unlock()
		return entry, nil
	}
	s.kataProxyMu.Unlock()

	entry, err := newKataDaemonProxyEntry(d)
	if err != nil {
		return kataProxyCacheEntry{}, err
	}

	s.kataProxyMu.Lock()
	if s.kataProxyCache == nil {
		s.kataProxyCache = make(map[kataProxyCacheKey]kataProxyCacheEntry)
	}
	if existing, ok := s.kataProxyCache[key]; ok {
		s.kataProxyMu.Unlock()
		if entry.closeIdle != nil {
			entry.closeIdle()
		}
		return existing, nil
	}
	s.kataProxyCache[key] = entry
	s.kataProxyMu.Unlock()
	return entry, nil
}

func (s *Server) closeKataProxyIdleConnections() {
	s.kataProxyMu.Lock()
	entries := make([]kataProxyCacheEntry, 0, len(s.kataProxyCache))
	for _, entry := range s.kataProxyCache {
		entries = append(entries, entry)
	}
	s.kataProxyMu.Unlock()

	for _, entry := range entries {
		if entry.closeIdle != nil {
			entry.closeIdle()
		}
	}
}

func (s *Server) selectKataProxyDaemon(w http.ResponseWriter, r *http.Request) (kata.Daemon, bool) {
	selected, problem := selectKataDaemonForID(r.Header.Get(kataDaemonHeaderName))
	if problem != nil {
		writeProblemResponse(w, problem)
		return kata.Daemon{}, false
	}
	return selected, true
}

// selectKataDaemonForID resolves the catalog daemon addressed by the
// X-Middleman-Kata-Daemon header value (the effective default daemon when
// empty) to a reachable target. Shared by the passthrough proxy and the
// server-side Kata task reads.
func selectKataDaemonForID(headerID string) (kata.Daemon, *ProblemError) {
	catalog, err := kata.LoadCatalog()
	if err != nil {
		return kata.Daemon{}, newProblem(
			http.StatusBadRequest,
			CodeBadRequest,
			err.Error(),
			nil,
		)
	}
	if len(catalog.Daemons) == 0 {
		return kata.Daemon{}, newProblem(
			http.StatusServiceUnavailable,
			CodeServiceUnavailable,
			"no Kata daemon configured",
			nil,
		)
	}

	id := strings.TrimSpace(headerID)
	if id == "" {
		id = effectiveKataDefaultID(catalog.Daemons)
	}
	var configured kata.Daemon
	found := false
	for _, d := range catalog.Daemons {
		if d.ID == id {
			configured = d
			found = true
			break
		}
	}
	if !found {
		return kata.Daemon{}, newProblem(
			http.StatusBadRequest,
			CodeBadRequest,
			"unknown Kata daemon",
			map[string]any{"daemon": id},
		)
	}

	selected, err := kata.ResolveDaemon(configured)
	if err != nil {
		return kata.Daemon{}, newProblem(
			http.StatusBadRequest,
			CodeBadRequest,
			err.Error(),
			map[string]any{"daemon": configured.ID},
		)
	}
	if selected.Local && selected.URL == "" {
		selected.URL = kata.DiscoverLocalDaemonURL()
		if selected.URL != "" {
			if err := kata.ValidateLocalTarget(selected); err != nil {
				slog.Warn("kata local daemon target rejected",
					"daemon", selected.ID, "target", kata.RedactURL(selected.URL), "err", err)
				selected.URL = ""
			}
		}
	}
	if selected.URL == "" {
		return kata.Daemon{}, newProblem(
			http.StatusServiceUnavailable,
			CodeServiceUnavailable,
			"Kata daemon is not reachable",
			map[string]any{"daemon": selected.ID},
		)
	}
	return selected, nil
}

func newKataDaemonProxyEntry(d kata.Daemon) (kataProxyCacheEntry, error) {
	return newKataDaemonProxyEntryWithTimeout(d, kataDaemonProxyRequestTimeout)
}

func newKataDaemonProxyEntryWithTimeout(d kata.Daemon, requestTimeout time.Duration) (kataProxyCacheEntry, error) {
	target, transport, err := kataDaemonProxyTarget(d.URL)
	if err != nil {
		return kataProxyCacheEntry{}, err
	}
	if transport == nil {
		transport = newDefaultKataDaemonTransport()
	}

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Origin")
			pr.Out.Header.Del(kataDaemonHeaderName)
			if d.Local {
				pr.Out.Header.Del("Authorization")
				if token := kataDaemonForwardToken(d); token != "" {
					pr.Out.Header.Set("Authorization", "Bearer "+token)
				}
				return
			}
			if token := kataDaemonForwardToken(d); token != "" && pr.Out.Header.Get("Authorization") == "" {
				pr.Out.Header.Set("Authorization", "Bearer "+token)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if !isKataLocalDaemonChallenge(d, resp.StatusCode) {
				return nil
			}
			problem := newProblem(
				http.StatusBadGateway,
				CodeUpstreamError,
				"Kata daemon is unreachable",
				map[string]any{"daemon": d.ID},
			)
			body, err := json.Marshal(problem)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			resp.StatusCode = problem.Status
			resp.Status = "502 Bad Gateway"
			resp.Header.Del("Content-Encoding")
			resp.Header.Del("Content-Language")
			resp.Header.Del("Content-Location")
			resp.Header.Del("ETag")
			resp.Header.Del("Last-Modified")
			resp.Header.Del("Trailer")
			resp.Header.Del("WWW-Authenticate")
			resp.Header.Set("Content-Type", "application/problem+json")
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			resp.Trailer = nil
			resp.ContentLength = int64(len(body))
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return nil
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			cause := err
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				cause = urlErr.Err
			}
			slog.Warn("kata proxy failed",
				"daemon", d.ID, "target", kata.RedactURL(d.URL), "err", cause)
			writeProblemResponse(w, newProblem(
				http.StatusBadGateway,
				CodeUpstreamError,
				"Kata daemon is unreachable",
				map[string]any{"daemon": d.ID},
			))
		},
	}

	var closeIdle func()
	if idleCloser, ok := transport.(interface{ CloseIdleConnections() }); ok {
		closeIdle = idleCloser.CloseIdleConnections
	}
	handler := http.Handler(proxy)
	if requestTimeout > 0 {
		handler = &kataProxyDeadlineHandler{proxy: proxy, requestTimeout: requestTimeout}
	}
	return kataProxyCacheEntry{handler: handler, closeIdle: closeIdle}, nil
}

func kataDaemonProxyTarget(target string) (*url.URL, http.RoundTripper, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, nil, err
	}
	switch parsed.Scheme {
	case "http", "https":
		if strings.TrimSpace(parsed.Hostname()) == "" {
			return nil, nil, errors.New("daemon url must include a host")
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed, nil, nil
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

func newDefaultKataDaemonTransport() http.RoundTripper {
	return (&http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}).Clone()
}

func disposableKataDaemonTransport(transport http.RoundTripper) http.RoundTripper {
	concrete, ok := transport.(*http.Transport)
	if !ok {
		return transport
	}
	owned := concrete.Clone()
	owned.DisableKeepAlives = true
	return owned
}
