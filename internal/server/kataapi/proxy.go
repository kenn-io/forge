package kataapi

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

	"go.kenn.io/forge/internal/kata"
	"go.kenn.io/forge/internal/server/httpapi"
)

const (
	kataDaemonHeaderName          = "X-Kenn-Forge-Kata-Daemon"
	kataProxyPrefix               = "/api/v1/kata/proxy"
	kataDaemonProxyRequestTimeout = 30 * time.Second
)

// DaemonHeaderName selects the Kata daemon used for a request.
const DaemonHeaderName = kataDaemonHeaderName

// DaemonCacheKeyDelim separates daemon identity components in cache keys.
const DaemonCacheKeyDelim = kataDaemonCacheKeyDelim

// IsProxyPath reports whether an API path belongs to Kata's passthrough
// boundary. Root middleware uses this to preserve the proxy's content-type
// and same-origin handling without owning Kata route details.
func IsProxyPath(path string) bool {
	return path == kataProxyPrefix || strings.HasPrefix(path, kataProxyPrefix+"/")
}

func isKataMutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// DaemonForwardToken returns the bearer token configured for daemon reads.
func DaemonForwardToken(d kata.Daemon) string {
	return kataDaemonForwardToken(d)
}

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

func (h *Handler) kataProxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", kataDaemonHeaderName)

		selected, ok := h.selectKataProxyDaemon(w, r)
		if !ok {
			return
		}
		entry, err := h.kataProxyForDaemon(selected)
		if err != nil {
			slog.Warn("kata proxy target invalid",
				"daemon", selected.ID, "target", kata.RedactURL(selected.URL), "err", err)
			writeProblemResponse(w, httpapi.NewProblem(
				http.StatusBadRequest,
				httpapi.CodeBadRequest,
				"invalid Kata daemon target",
				map[string]any{"daemon": selected.ID},
			))
			return
		}

		http.StripPrefix(kataProxyPrefix, entry.handler).ServeHTTP(w, r)
	})
}

func (h *Handler) kataProxyForDaemon(d kata.Daemon) (kataProxyCacheEntry, error) {
	key := kataProxyCacheKey{id: d.ID, url: d.URL, token: kataDaemonForwardToken(d), local: d.Local}
	h.kataProxyMu.Lock()
	if entry, ok := h.kataProxyCache[key]; ok {
		h.kataProxyMu.Unlock()
		return entry, nil
	}
	h.kataProxyMu.Unlock()

	entry, err := newKataDaemonProxyEntryWithTransport(
		d, kataDaemonProxyRequestTimeout, h.newHTTPTransport(), func() {
			if h.invalidateDaemon != nil {
				h.invalidateDaemon(d.ID)
			}
		},
	)
	if err != nil {
		return kataProxyCacheEntry{}, err
	}

	h.kataProxyMu.Lock()
	if h.kataProxyCache == nil {
		h.kataProxyCache = make(map[kataProxyCacheKey]kataProxyCacheEntry)
	}
	if existing, ok := h.kataProxyCache[key]; ok {
		h.kataProxyMu.Unlock()
		if entry.closeIdle != nil {
			entry.closeIdle()
		}
		return existing, nil
	}
	h.kataProxyCache[key] = entry
	h.kataProxyMu.Unlock()
	return entry, nil
}

func (h *Handler) closeKataProxyIdleConnections() {
	h.kataProxyMu.Lock()
	entries := make([]kataProxyCacheEntry, 0, len(h.kataProxyCache))
	for _, entry := range h.kataProxyCache {
		entries = append(entries, entry)
	}
	h.kataProxyMu.Unlock()

	for _, entry := range entries {
		if entry.closeIdle != nil {
			entry.closeIdle()
		}
	}
}

func (h *Handler) selectKataProxyDaemon(w http.ResponseWriter, r *http.Request) (kata.Daemon, bool) {
	selected, problem := h.selectKataDaemonForID(r.Header.Get(kataDaemonHeaderName))
	if problem != nil {
		writeProblemResponse(w, problem)
		return kata.Daemon{}, false
	}
	return selected, true
}

// selectKataDaemonForID resolves the catalog daemon addressed by the
// X-Kenn-Forge-Kata-Daemon header value (the effective default daemon when
// empty) to a reachable target. Shared by the passthrough proxy and the
// server-side Kata task reads.
func (h *Handler) selectKataDaemonForID(headerID string) (kata.Daemon, *httpapi.ProblemError) {
	catalog, err := h.loadCatalog()
	if err != nil {
		return kata.Daemon{}, httpapi.NewProblem(
			http.StatusBadRequest,
			httpapi.CodeBadRequest,
			err.Error(),
			nil,
		)
	}
	if len(catalog.Daemons) == 0 {
		return kata.Daemon{}, httpapi.NewProblem(
			http.StatusServiceUnavailable,
			httpapi.CodeServiceUnavailable,
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
		return kata.Daemon{}, httpapi.NewProblem(
			http.StatusBadRequest,
			httpapi.CodeBadRequest,
			"unknown Kata daemon",
			map[string]any{"daemon": id},
		)
	}

	selected, err := h.resolveDaemon(configured)
	if err != nil {
		return kata.Daemon{}, httpapi.NewProblem(
			http.StatusBadRequest,
			httpapi.CodeBadRequest,
			err.Error(),
			map[string]any{"daemon": configured.ID},
		)
	}
	if selected.Local && selected.URL == "" {
		selected.URL = h.discoverLocalDaemonURL()
		if selected.URL != "" {
			if err := kata.ValidateLocalTarget(selected); err != nil {
				slog.Warn("kata local daemon target rejected",
					"daemon", selected.ID, "target", kata.RedactURL(selected.URL), "err", err)
				selected.URL = ""
			}
		}
	}
	if selected.URL == "" {
		return kata.Daemon{}, httpapi.NewProblem(
			http.StatusServiceUnavailable,
			httpapi.CodeServiceUnavailable,
			"Kata daemon is not reachable",
			map[string]any{"daemon": selected.ID},
		)
	}
	return selected, nil
}

// SelectDaemonForID resolves the configured daemon for root-owned Kata
// frontend streaming routes.
func (h *Handler) SelectDaemonForID(id string) (kata.Daemon, *httpapi.ProblemError) {
	return h.selectKataDaemonForID(id)
}

// ResolveDefaultDaemonForID resolves a daemon using the standard catalog and
// runtime discovery dependencies.
func ResolveDefaultDaemonForID(id string) (kata.Daemon, *httpapi.ProblemError) {
	return New(Deps{}).selectKataDaemonForID(id)
}

func newKataDaemonProxyEntryWithTimeout(d kata.Daemon, requestTimeout time.Duration) (kataProxyCacheEntry, error) {
	return newKataDaemonProxyEntryWithTransport(
		d, requestTimeout, newDefaultKataDaemonTransport(), nil,
	)
}

func newKataDaemonProxyEntryWithTransport(
	d kata.Daemon,
	requestTimeout time.Duration,
	defaultTransport http.RoundTripper,
	invalidate func(),
) (kataProxyCacheEntry, error) {
	target, transport, err := kataDaemonProxyTarget(d.URL)
	if err != nil {
		return kataProxyCacheEntry{}, err
	}
	if transport == nil {
		transport = defaultTransport
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
			if invalidate != nil && isKataMutationMethod(resp.Request.Method) && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				invalidate()
			}
			if !isKataLocalDaemonChallenge(d, resp.StatusCode) {
				return nil
			}
			problem := httpapi.NewProblem(
				http.StatusBadGateway,
				httpapi.CodeUpstreamError,
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
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			cause := err
			var urlErr *url.Error
			if errors.As(err, &urlErr) {
				cause = urlErr.Err
			}
			slog.Warn("kata proxy failed",
				"daemon", d.ID, "target", kata.RedactURL(d.URL), "err", cause)
			code := httpapi.CodeUpstreamError
			detail := "Kata daemon is unreachable"
			if isKataMutationMethod(r.Method) {
				if invalidate != nil {
					invalidate()
				}
				code = httpapi.CodeMutationOutcomeUnknown
				detail = "Kata could not confirm whether the mutation was applied."
			}
			writeProblemResponse(w, httpapi.NewProblem(
				http.StatusBadGateway,
				code,
				detail,
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
