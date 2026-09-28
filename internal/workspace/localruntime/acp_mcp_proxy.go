package localruntime

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// ACPMCPBinding changes when the Forge daemon restarts. Agents keep the owner's
// stable loopback URL and token for the lifetime of their ACP session.
type ACPMCPBinding struct {
	URL   string
	Token string
}

type acpMCPProxy struct {
	mu       sync.RWMutex
	binding  ACPMCPBinding
	server   *http.Server
	listener net.Listener
	token    string
}

func newACPMCPProxy(binding ACPMCPBinding) (*acpMCPProxy, error) {
	p := &acpMCPProxy{token: rand.Text()}
	if err := p.Bind(binding); err != nil {
		return nil, err
	}
	if binding.URL == "" {
		return p, nil
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p.listener = listener
	p.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+p.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		p.mu.RLock()
		binding := p.binding
		p.mu.RUnlock()
		if binding.URL == "" {
			http.Error(w, "Forge is unavailable", http.StatusServiceUnavailable)
			return
		}
		proxy := &httputil.ReverseProxy{Rewrite: func(request *httputil.ProxyRequest) {
			upstream, _ := url.Parse(binding.URL)
			request.SetURL(upstream)
			request.Out.URL.Path = upstream.Path
			request.Out.Host = upstream.Host
			request.Out.Header.Set("Authorization", "Bearer "+binding.Token)
			// The authenticated proxy hop has a different loopback origin.
			request.Out.Header.Del("Origin")
		}}
		proxy.ServeHTTP(w, r)
	})}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *acpMCPProxy) Bind(binding ACPMCPBinding) error {
	if binding.URL != "" {
		upstream, err := url.Parse(binding.URL)
		if err != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" {
			return errors.New("invalid ACP Forge MCP endpoint")
		}
	}
	p.mu.Lock()
	p.binding = binding
	p.mu.Unlock()
	return nil
}

func (p *acpMCPProxy) URL() string {
	if p.listener == nil {
		return ""
	}
	return "http://" + p.listener.Addr().String() + "/mcp"
}

func (p *acpMCPProxy) Close() {
	if p.server != nil {
		_ = p.server.Close()
	}
}
