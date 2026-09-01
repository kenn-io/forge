package fleetsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"go.kenn.io/forge/internal/config"
)

type tailscaleDiscovery struct {
	DNSName string
	Login   string
}

type tailscaleStatus struct {
	BackendState string `json:"BackendState"`
	Self         struct {
		DNSName string `json:"DNSName"`
		UserID  int64  `json:"UserID"`
	} `json:"Self"`
	Users map[string]struct {
		LoginName string `json:"LoginName"`
	} `json:"User"`
	CertDomains []string `json:"CertDomains"`
}

func (r *Runner) discoverTailscale(
	ctx context.Context,
	command string,
	explicitDNS string,
	explicitLogin string,
) (tailscaleDiscovery, error) {
	result, err := r.deps.run(ctx, command, "status", "--json")
	if err != nil {
		return tailscaleDiscovery{}, fmt.Errorf("read Tailscale status: %w", err)
	}
	var status tailscaleStatus
	if err := json.Unmarshal(result.stdout, &status); err != nil {
		return tailscaleDiscovery{}, fmt.Errorf("decode Tailscale status: %w", err)
	}
	if status.BackendState != "Running" {
		return tailscaleDiscovery{}, fmt.Errorf("tailscale backend is %q, expected Running", status.BackendState)
	}
	dnsName := canonicalDNSName(explicitDNS)
	if dnsName == "" {
		dnsName = canonicalDNSName(status.Self.DNSName)
	}
	if err := validateDNSName(dnsName); err != nil {
		return tailscaleDiscovery{}, err
	}
	if explicitDNS == "" && len(status.CertDomains) > 0 && !containsDNS(status.CertDomains, dnsName) {
		return tailscaleDiscovery{}, fmt.Errorf("tailscale HTTPS certificate is not available for %q", dnsName)
	}

	login := strings.TrimSpace(explicitLogin)
	if login == "" {
		profile, ok := status.Users[strconv.FormatInt(status.Self.UserID, 10)]
		if !ok {
			return tailscaleDiscovery{}, errors.New("tailscale status did not identify the current device user")
		}
		login = profile.LoginName
	}
	normalizedLogin, err := config.NormalizeTailscaleLogin(login)
	if err != nil {
		return tailscaleDiscovery{}, fmt.Errorf("tailscale login: %w", err)
	}
	return tailscaleDiscovery{DNSName: dnsName, Login: normalizedLogin}, nil
}

func canonicalDNSName(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}

func validateDNSName(name string) error {
	if name == "" || strings.ContainsAny(name, "/:@[]") || net.ParseIP(name) != nil {
		return fmt.Errorf("invalid canonical Tailscale DNS name %q", name)
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return fmt.Errorf("invalid canonical Tailscale DNS name %q", name)
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid canonical Tailscale DNS name %q", name)
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return fmt.Errorf("invalid canonical Tailscale DNS name %q", name)
			}
		}
	}
	return nil
}

func containsDNS(values []string, expected string) bool {
	for _, value := range values {
		if canonicalDNSName(value) == expected {
			return true
		}
	}
	return false
}

type serveState int

const (
	serveAbsent serveState = iota
	serveExact
)

type serveStatus struct {
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string `json:"Proxy"`
		} `json:"Handlers"`
	} `json:"Web"`
}

func (r *Runner) inspectServe(
	ctx context.Context,
	command string,
	dnsName string,
	port int,
) (serveState, error) {
	result, err := r.deps.run(ctx, command, "serve", "status", "--json")
	if err != nil {
		return serveAbsent, fmt.Errorf("read Tailscale Serve status: %w", err)
	}
	var status serveStatus
	if err := json.Unmarshal(result.stdout, &status); err != nil {
		return serveAbsent, fmt.Errorf("decode Tailscale Serve status: %w", err)
	}
	expected := fmt.Sprintf("http://127.0.0.1:%d", port)
	for authority, web := range status.Web {
		handler, ok := web.Handlers["/"]
		if !ok {
			continue
		}
		host, rawPort, err := net.SplitHostPort(authority)
		if err != nil || canonicalDNSName(host) != dnsName || rawPort != "443" {
			continue
		}
		if handler.Proxy == expected {
			return serveExact, nil
		}
		return serveAbsent, fmt.Errorf("tailscale Serve already owns https://%s/ with proxy %q", dnsName, handler.Proxy)
	}
	return serveAbsent, nil
}

func (r *Runner) applyServe(ctx context.Context, plan Plan, transaction *transaction) error {
	command := plan.tailscaleCommand
	if command == "" {
		command = "tailscale"
	}
	target := fmt.Sprintf("http://127.0.0.1:%d", plan.Port)
	if _, err := r.deps.run(
		ctx, command, "serve", "--yes", "--bg", "--https=443", target,
	); err != nil {
		return fmt.Errorf("configure Tailscale Serve: %w", err)
	}
	transaction.record(func(ctx context.Context) error {
		_, err := r.deps.run(
			ctx, command, "serve", "--yes", "--https=443", "--set-path=/", "off",
		)
		return err
	})
	return nil
}

func (r *Runner) resolveTailscaleCommand() (string, error) {
	if _, err := r.deps.lookupPath("tailscale"); err == nil {
		return "tailscale", nil
	}
	if r.deps.goos == "darwin" {
		info, err := r.deps.stat(r.deps.tailscaleAppPath)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return r.deps.tailscaleAppPath, nil
		}
	}
	return "", errors.New("tailscale CLI is not installed")
}
