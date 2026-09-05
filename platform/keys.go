package platform

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeHost returns the provider instance key, not a transport URL.
// The provider must be explicit; only an empty host selects its default.
func NormalizeHost(kind Kind, input string) (string, error) {
	host, err := normalizeHost(kind, input)
	if err != nil {
		return "", &Error{Code: ErrCodeInvalidRepoRef, Provider: kind, Field: "platform_host", Err: err}
	}
	return host, nil
}

func normalizeHost(kind Kind, input string) (string, error) {
	metadata, ok := builtInMetadata[kind]
	if !ok {
		return "", fmt.Errorf("unknown or noncanonical provider %q", kind)
	}
	host := strings.ToLower(strings.TrimSpace(input))
	if host == "" {
		host = metadata.DefaultHost
	}
	if strings.ContainsAny(host, "?#") {
		return "", fmt.Errorf("provider instance must not contain a query or fragment")
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		origin, err := url.Parse(host)
		if err != nil {
			return "", fmt.Errorf("invalid provider origin: %w", err)
		}
		if origin.User != nil || (origin.Path != "" && origin.Path != "/") {
			return "", fmt.Errorf("provider origin must not contain userinfo or a subpath")
		}
		host = origin.Host
	}
	// Validate syntax before removing the TLS port alias. In particular an
	// invalid signed port must not become valid through integer parsing.
	if err := validateCanonicalRepoPort(host); err != nil {
		return "", err
	}
	parsed, err := url.Parse("//" + host)
	if err != nil {
		return "", fmt.Errorf("invalid provider authority: %w", err)
	}
	if port, err := strconv.Atoi(parsed.Port()); err == nil && port == 443 {
		host = strings.TrimSuffix(host, ":"+parsed.Port())
	}
	if err := validateCanonicalRepoHost(host); err != nil {
		return "", err
	}
	return host, nil
}

// NormalizeGitEmail fixes the exact byte key for a Git email claim. It does
// not validate declarations or infer provider-specific address equivalence.
func NormalizeGitEmail(input string) string {
	const whitespace = " \t\r\n\v\f"
	value := strings.Trim(input, whitespace)
	if len(value) >= 2 && value[0] == '<' && value[len(value)-1] == '>' {
		value = value[1 : len(value)-1]
	}
	value = strings.Trim(value, whitespace)
	result := []byte(value)
	for i, ch := range result {
		if ch >= 'A' && ch <= 'Z' {
			result[i] = ch + ('a' - 'A')
		}
	}
	return string(result)
}
