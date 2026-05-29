package profiler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxExpensiveProfileSeconds = 30

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
		Handler: allowBoundHostOnly(
			limitExpensiveProfiles(rejectCrossSiteBrowserRequests(NewHandler())),
			ln.Addr(),
		),
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
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf(
			"profiler address %s must bind to a literal loopback IP",
			addr,
		)
	}
	return nil
}

func allowBoundHostOnly(next http.Handler, addr net.Addr) http.Handler {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok || tcpAddr.IP == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "profiler host not allowed", http.StatusForbidden)
		})
	}
	allowedIP := tcpAddr.IP
	allowedPort := fmt.Sprint(tcpAddr.Port)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil {
			http.Error(w, "profiler host not allowed", http.StatusForbidden)
			return
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.Equal(allowedIP) || port != allowedPort {
			http.Error(w, "profiler host not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func rejectCrossSiteBrowserRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchSite := r.Header.Get("Sec-Fetch-Site")
		origin := r.Header.Get("Origin")

		switch fetchSite {
		case "", "none", "same-origin":
		default:
			http.Error(
				w,
				"profiler cross-site browser request not allowed",
				http.StatusForbidden,
			)
			return
		}

		if origin != "" {
			originURL, err := url.Parse(origin)
			if err != nil || originURL.Scheme != "http" || originURL.Host != r.Host {
				http.Error(
					w,
					"profiler origin not allowed",
					http.StatusForbidden,
				)
				return
			}
		}

		if fetchSite == "" && origin == "" && isBrowserUserAgent(r.UserAgent()) {
			http.Error(
				w,
				"profiler browser request metadata required",
				http.StatusForbidden,
			)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isBrowserUserAgent(userAgent string) bool {
	userAgent = strings.ToLower(userAgent)
	for _, marker := range []string{
		"mozilla/",
		"firefox/",
		"chrome/",
		"chromium/",
		"safari/",
		"edg/",
		"opr/",
	} {
		if strings.Contains(userAgent, marker) {
			return true
		}
	}
	return false
}

func limitExpensiveProfiles(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/debug/pprof/") {
			next.ServeHTTP(w, r)
			return
		}

		seconds := r.URL.Query().Get("seconds")
		if seconds == "" {
			next.ServeHTTP(w, r)
			return
		}
		value, err := strconv.Atoi(seconds)
		if err == nil && value > maxExpensiveProfileSeconds {
			http.Error(
				w,
				fmt.Sprintf(
					"profiler seconds must be <= %d",
					maxExpensiveProfileSeconds,
				),
				http.StatusBadRequest,
			)
			return
		}

		next.ServeHTTP(w, r)
	})
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
