package profiler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"
)

// Server owns the optional diagnostics HTTP listener.
type Server struct {
	httpSrv *http.Server
	ln      net.Listener
	done    chan error
}

// NewHandler returns a mux with the standard net/http/pprof endpoints.
func NewHandler() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// Start begins serving the standard profiler endpoints on addr.
func Start(addr string) (*Server, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, nil
	}
	if err := validateLoopbackAddress(addr); err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on profiler address %s: %w", addr, err)
	}

	httpSrv := &http.Server{
		Handler:           NewHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	srv := &Server{
		httpSrv: httpSrv,
		ln:      ln,
		done:    make(chan error, 1),
	}
	go func() {
		err := httpSrv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		srv.done <- err
	}()
	return srv, nil
}

func validateLoopbackAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid profiler address %s: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf(
			"profiler address %s must bind to a loopback host",
			addr,
		)
	}
	return nil
}

// Addr returns the bound listener address.
func (s *Server) Addr() net.Addr {
	if s == nil || s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Done reports unexpected serving errors.
func (s *Server) Done() <-chan error {
	if s == nil {
		return nil
	}
	return s.done
}

// Shutdown stops the diagnostics listener and waits for Serve to return.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	shutdownErr := s.httpSrv.Shutdown(ctx)
	select {
	case serveErr := <-s.done:
		if shutdownErr != nil {
			return shutdownErr
		}
		return serveErr
	case <-ctx.Done():
		if shutdownErr != nil {
			return shutdownErr
		}
		return ctx.Err()
	}
}
