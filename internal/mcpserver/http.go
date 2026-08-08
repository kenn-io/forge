package mcpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const httpShutdownTimeout = 5 * time.Second

func (s *Server) RunHTTP(ctx context.Context) error {
	tokenEnv := strings.TrimSpace(s.opts.HTTPTokenEnv)
	if tokenEnv == "" {
		return fmt.Errorf("--http-token-env is required for http transport")
	}
	token := strings.TrimSpace(os.Getenv(tokenEnv))
	if token == "" {
		return fmt.Errorf("HTTP token env %s is unset or blank", tokenEnv)
	}
	addr := strings.TrimSpace(s.opts.Addr)
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid HTTP listen address %q: %w", addr, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("HTTP MCP transport only supports loopback listen addresses")
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen for HTTP MCP transport: %w", err)
	}
	actualPort, err := listenerPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return err
	}
	s.setHTTPListenAddr(ln.Addr().String())
	defer s.setHTTPListenAddr("")

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s.mcp
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	httpSrv := &http.Server{
		Handler:           s.httpGuard(mcpHandler, token, actualPort),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	shutdownErrc := make(chan error, 1)
	go func() {
		<-serveCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer shutdownCancel()
		shutdownErrc <- httpSrv.Shutdown(shutdownCtx)
	}()

	err = httpSrv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		if ctx.Err() != nil {
			if shutdownErr := <-shutdownErrc; shutdownErr != nil {
				return shutdownErr
			}
		}
		return nil
	}
	return err
}

func (s *Server) httpGuard(next http.Handler, token string, port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasBearerToken(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !sameLoopbackPort(r.Host, port) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !sameHTTPOrigin(origin, port) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) setHTTPListenAddr(addr string) {
	s.httpMu.Lock()
	defer s.httpMu.Unlock()
	s.httpAddr = addr
}

func (s *Server) httpListenAddr() string {
	s.httpMu.RLock()
	defer s.httpMu.RUnlock()
	return s.httpAddr
}

func hasBearerToken(r *http.Request, token string) bool {
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	want := "Bearer " + token
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func sameHTTPOrigin(origin string, port int) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" {
		return false
	}
	return sameLoopbackPort(u.Host, port)
}

func sameLoopbackPort(hostport string, port int) bool {
	host, portText, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	gotPort, err := strconv.Atoi(portText)
	if err != nil || gotPort != port {
		return false
	}
	return isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func listenerPort(addr string) (int, error) {
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("read HTTP MCP listener address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0, fmt.Errorf("read HTTP MCP listener port: %w", err)
	}
	return port, nil
}
