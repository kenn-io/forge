package landedwork

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"go.kenn.io/forge/platform"
)

type RouteWarning struct {
	Reason        string `json:"reason"`
	RemoteRoute   string `json:"remote_route"`
	ProviderRoute string `json:"provider_route"`
}

type Correspondence struct {
	ProviderHead string         `json:"provider_head"`
	AnalysisHead string         `json:"analysis_head"`
	Complete     bool           `json:"complete"`
	Reason       string         `json:"reason"`
	Warnings     []RouteWarning `json:"warnings"`
}

// CheckCorrespondence checks only the explicitly selected origin fetch remote
// and local objects. It never discovers credentials, follows a URL, fetches, or
// changes the remote. The descriptor, not shared Git content, owns identity.
func (g *Git) CheckCorrespondence(ctx context.Context, descriptor platform.LandingRepository, head string, meter *platform.Meter) (Correspondence, error) {
	result := Correspondence{ProviderHead: descriptor.HeadSHA, AnalysisHead: head}
	if !fullObjectID(head) || !fullObjectID(descriptor.HeadSHA) || descriptor.Identity.ID == "" {
		return result, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "repository_correspondence"}
	}
	host, err := platform.NormalizeHost(descriptor.Identity.Provider, descriptor.Identity.Instance)
	if err != nil || host != descriptor.Identity.Instance {
		return result, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "instance"}
	}
	// Use the original config for this one offline scalar read only. Includes,
	// URL rewrites and Git's transport layer do not participate.
	local := &Git{repository: g.repository}
	data, err := local.run(ctx, meter, nil, "config", "--no-includes", "--local", "--get-all", "remote.origin.url")
	if err != nil {
		return result, err
	}
	remote := strings.TrimSuffix(string(data), "\n")
	remoteHost, route, ssh, err := fetchRoute(remote)
	if err != nil {
		return result, err
	}
	if ssh {
		instance, parseErr := url.Parse("https://" + host)
		if parseErr != nil || !strings.EqualFold(remoteHost, instance.Hostname()) {
			return result, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "remote_host"}
		}
	} else {
		normalized, normalizeErr := platform.NormalizeHost(descriptor.Identity.Provider, remoteHost)
		if normalizeErr != nil || normalized != host {
			return result, &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "remote_host"}
		}
	}
	providerRoute := descriptor.Owner + "/" + descriptor.Name
	if route != providerRoute {
		result.Warnings = []RouteWarning{{Reason: "remote_route_differs", RemoteRoute: host + "/" + route, ProviderRoute: host + "/" + providerRoute}}
	}
	graph := &graph{source: g, meter: meter, commits: make(map[string]Commit)}
	_, err = graph.get(ctx, head)
	if err == nil {
		_, err = graph.get(ctx, descriptor.HeadSHA)
	}
	var ancestor bool
	if err == nil {
		ancestor, err = graph.isAncestor(ctx, head, descriptor.HeadSHA)
	}
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Reason = "correspondence_objects_unavailable"
		if errors.Is(err, platform.ErrPageLimit) {
			result.Reason = "correspondence_budget_exhausted"
		}
		return result, nil
	}
	if !ancestor {
		return result, &platform.Error{Code: platform.ErrCodeConflict, Field: "analysis_head"}
	}
	result.Complete = true
	return result, nil
}

func fetchRoute(remote string) (host, route string, ssh bool, err error) {
	failure := &platform.Error{Code: platform.ErrCodeInvalidArgument, Field: "origin_fetch_remote"}
	if strings.ContainsAny(remote, "\x00\r\n\t ") {
		return "", "", false, failure
	}
	if !strings.Contains(remote, "://") {
		// SCP-style SSH; its colon separates a path, never an API port.
		authority, path, ok := strings.Cut(remote, ":")
		if !ok || !strings.Contains(authority, "@") {
			return "", "", false, failure
		}
		_, host, _ = strings.Cut(authority, "@")
		route, ssh = path, true
	} else {
		parsed, parseErr := url.Parse(remote)
		if parseErr != nil || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", false, failure
		}
		switch parsed.Scheme {
		case "https", "http":
			host = parsed.Host
		case "ssh":
			host, ssh = parsed.Hostname(), true
		default:
			return "", "", false, failure
		}
		route = strings.TrimPrefix(parsed.Path, "/")
	}
	route = strings.TrimSuffix(route, ".git")
	if host == "" || !strings.Contains(route, "/") || strings.HasPrefix(route, "/") || strings.HasSuffix(route, "/") {
		return "", "", false, failure
	}
	return host, route, ssh, nil
}
