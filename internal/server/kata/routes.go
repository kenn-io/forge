package kata

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	katacatalog "go.kenn.io/forge/internal/kata"
	"go.kenn.io/forge/internal/server/httpapi"
)

const (
	kataDaemonHealthTTL     = 5 * time.Second
	kataDaemonProbeTimeout  = 2 * time.Second
	kataDaemonCacheKeyDelim = "\x00"
	kataDaemonHeaderName    = "X-Kenn-Forge-Kata-Daemon"
)

// DaemonForwardToken returns the bearer token configured for daemon reads.
func DaemonForwardToken(d katacatalog.Daemon) string {
	return kataDaemonForwardToken(d)
}

type kataDaemonResponse struct {
	ID               string `json:"id"`
	URL              string `json:"url"`
	Default          bool   `json:"default"`
	Auth             string `json:"auth"`
	Health           string `json:"health"`
	APISchemaVersion string `json:"api_schema_version,omitempty"`
	Hint             string `json:"hint,omitempty"`
}

type kataDaemonRosterResponse struct {
	Daemons []kataDaemonResponse `json:"daemons"`
	Source  string               `json:"source,omitempty"`
}

type listKataDaemonsOutput = httpapi.BodyOutput[kataDaemonRosterResponse]

type kataDaemonHealthCacheEntry struct {
	health  kataDaemonHealth
	expires time.Time
}

type kataDaemonInflightProbe struct {
	wg     sync.WaitGroup
	result kataDaemonHealth
}

func (h *Handler) listKataDaemons(context.Context, *struct{}) (*listKataDaemonsOutput, error) {
	catalog, err := h.loadCatalog()
	if err != nil {
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}

	resolved := make([]katacatalog.Daemon, len(catalog.Daemons))
	for i, configured := range catalog.Daemons {
		d, err := h.resolveDaemon(configured)
		if err != nil {
			return nil, httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
		}
		if d.Local && d.URL == "" {
			d.URL = h.discoverLocalDaemonURL()
		}
		resolved[i] = d
	}

	health := make([]kataDaemonHealth, len(resolved))
	var wg sync.WaitGroup
	wg.Add(len(resolved))
	for i, configured := range catalog.Daemons {
		go func() {
			defer wg.Done()
			health[i] = h.kataDaemonHealth(configured.ID, resolved[i])
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
		if kataDaemonForwardToken(d) != "" {
			auth = "token"
		}
		hint := ""
		if d.Local && d.URL == "" {
			hint = "local daemon not running; run `kata daemon start`"
		} else if health[i].State == "incompatible" {
			hint = kataDaemonCompatibilityMessage(health[i].APISchemaVersion)
		}

		out.Daemons = append(out.Daemons, kataDaemonResponse{
			ID:               configured.ID,
			URL:              katacatalog.RedactURL(d.URL),
			Default:          configured.ID == defaultID,
			Auth:             auth,
			Health:           health[i].State,
			APISchemaVersion: health[i].APISchemaVersion,
			Hint:             hint,
		})
	}

	return &listKataDaemonsOutput{Body: out}, nil
}

func effectiveKataDefaultID(daemons []katacatalog.Daemon) string {
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

func (h *Handler) selectKataDaemonForID(requestedID string) (katacatalog.Daemon, *httpapi.ProblemError) {
	catalog, err := h.loadCatalog()
	if err != nil {
		return katacatalog.Daemon{}, httpapi.NewProblem(
			http.StatusBadRequest, httpapi.CodeBadRequest, err.Error(), nil,
		)
	}
	if len(catalog.Daemons) == 0 {
		return katacatalog.Daemon{}, httpapi.NewProblem(
			http.StatusServiceUnavailable, httpapi.CodeServiceUnavailable, "no Kata daemon configured", nil,
		)
	}

	id := strings.TrimSpace(requestedID)
	if id == "" {
		id = effectiveKataDefaultID(catalog.Daemons)
	}
	var configured *katacatalog.Daemon
	for i := range catalog.Daemons {
		if catalog.Daemons[i].ID == id {
			configured = &catalog.Daemons[i]
			break
		}
	}
	if configured == nil {
		return katacatalog.Daemon{}, httpapi.NewProblem(
			http.StatusBadRequest, httpapi.CodeBadRequest, "unknown Kata daemon", map[string]any{"daemon": id},
		)
	}

	selected, err := h.resolveDaemon(*configured)
	if err != nil {
		return katacatalog.Daemon{}, httpapi.NewProblem(
			http.StatusBadRequest, httpapi.CodeBadRequest, err.Error(), map[string]any{"daemon": configured.ID},
		)
	}
	if selected.Local && selected.URL == "" {
		selected.URL = h.discoverLocalDaemonURL()
		if selected.URL != "" {
			if err := katacatalog.ValidateLocalTarget(selected); err != nil {
				slog.Warn("kata local daemon target rejected",
					"daemon", selected.ID, "target", katacatalog.RedactURL(selected.URL), "err", err)
				selected.URL = ""
			}
		}
	}
	if selected.URL == "" {
		return katacatalog.Daemon{}, httpapi.NewProblem(
			http.StatusServiceUnavailable,
			httpapi.CodeServiceUnavailable,
			"Kata daemon is not reachable",
			map[string]any{"daemon": selected.ID},
		)
	}
	return selected, nil
}

func (h *Handler) kataDaemonHealth(id string, d katacatalog.Daemon) kataDaemonHealth {
	if d.URL == "" {
		return kataDaemonHealth{State: "down"}
	}
	cacheKey := kataDaemonHealthCacheKey(id, d)

	h.kataHealthMu.Lock()
	if h.kataHealthCache == nil {
		h.kataHealthCache = map[string]kataDaemonHealthCacheEntry{}
	}
	if h.kataHealthInFlight == nil {
		h.kataHealthInFlight = map[string]*kataDaemonInflightProbe{}
	}
	if c, ok := h.kataHealthCache[cacheKey]; ok && time.Now().Before(c.expires) {
		health := c.health
		h.kataHealthMu.Unlock()
		return health
	}
	if fp, ok := h.kataHealthInFlight[cacheKey]; ok {
		h.kataHealthMu.Unlock()
		fp.wg.Wait()
		return fp.result
	}
	fp := &kataDaemonInflightProbe{}
	fp.wg.Add(1)
	h.kataHealthInFlight[cacheKey] = fp
	h.kataHealthMu.Unlock()

	health := h.probeKataDaemon(id, d)

	h.kataHealthMu.Lock()
	h.kataHealthCache[cacheKey] = kataDaemonHealthCacheEntry{
		health:  health,
		expires: time.Now().Add(kataDaemonHealthTTL),
	}
	delete(h.kataHealthInFlight, cacheKey)
	h.kataHealthMu.Unlock()

	fp.result = health
	fp.wg.Done()
	return health
}

func kataDaemonHealthCacheKey(id string, d katacatalog.Daemon) string {
	mode := "remote"
	if d.Local {
		mode = "local"
	}
	return strings.Join([]string{id, d.URL, mode, kataDaemonForwardToken(d)}, kataDaemonCacheKeyDelim)
}

func (h *Handler) probeKataDaemon(id string, d katacatalog.Daemon) kataDaemonHealth {
	client, baseURL, err := h.kataDaemonHTTPClient(d)
	if err != nil {
		slog.Warn("kata daemon health probe target invalid",
			"daemon", id, "target", katacatalog.RedactURL(d.URL), "err", err)
		return kataDaemonHealth{State: "down"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), kataDaemonProbeTimeout)
	defer cancel()
	health, err := (&kataDaemonClient{
		daemon: d, client: client, baseURL: baseURL,
	}).Health(ctx)
	if err != nil {
		slog.Warn("kata daemon health probe failed",
			"daemon", id, "target", katacatalog.RedactURL(d.URL), "err", err)
		return kataDaemonHealth{State: "down"}
	}
	return health
}

func isKataLocalDaemonChallenge(d katacatalog.Daemon, statusCode int) bool {
	return d.Local &&
		kataDaemonForwardToken(d) == "" &&
		(statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden)
}

func kataDaemonForwardToken(d katacatalog.Daemon) string {
	return d.Token
}
