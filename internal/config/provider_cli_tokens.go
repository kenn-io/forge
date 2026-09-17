package config

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"go.kenn.io/forge/internal/tokenauth"
	platformpkg "go.kenn.io/forge/platform"
)

// Provider CLI fallbacks for non-GitHub hosts. They mirror the `gh auth
// token` fallback: the user's locally authenticated CLI supplies a
// credential when no token_env or token_file is declared for the host.
//
//   - GitLab reads the token the glab CLI stores for the host. `glab config
//     get token --host HOST` prints the resolved token on stdout (plaintext
//     config or the operating-system keyring) and nothing when unset, and it
//     never contacts the server. `glab auth status --show-token` is not used:
//     it writes to stderr and exits non-zero when the API probe fails, so an
//     offline daemon would lose the credential.
//   - Forgejo and Gitea read the keys file the fj CLI (forgejo-cli) maintains.
//     fj has no command that prints a stored token, so the file is the only
//     lookup path. Entries are keyed by host without scheme, keeping any port
//     or subpath, and are either application tokens or OAuth tokens with an
//     expiry that fj refreshes on its own next run.

// cliSourceKindForPlatform returns the CLI candidate kind that serves a
// non-GitHub platform, or "" when no CLI fallback exists for it.
func cliSourceKindForPlatform(platform string) tokenauth.SourceKind {
	switch platform {
	case string(platformpkg.KindGitLab):
		return tokenauth.SourceKindGitLabCLI
	case string(platformpkg.KindForgejo), string(platformpkg.KindGitea):
		return tokenauth.SourceKindForgejoCLI
	default:
		return ""
	}
}

// providerCLITokenForHost resolves the CLI fallback for a non-GitHub
// platform host synchronously, mirroring ghAuthTokenForHost.
func providerCLITokenForHost(platform, host string) string {
	ctx, cancel := context.WithTimeout(context.Background(), ghAuthExecTimeout)
	defer cancel()
	var token string
	switch cliSourceKindForPlatform(platform) {
	case tokenauth.SourceKindGitLabCLI:
		token, _ = GitLabCLITokenForHost(ctx, host)
	case tokenauth.SourceKindForgejoCLI:
		token, _ = ForgejoCLITokenForHost(ctx, host)
	}
	return token
}

// GitLabCLITokenForHost returns the token glab holds for host, or "" when
// glab is missing, unauthenticated for that host, or fails. Like the gh
// fallback, a failed subprocess is a missing credential rather than an
// error so the caller surfaces the descriptor chain instead of a stderr
// transcript. The update check is disabled so the lookup stays offline.
func GitLabCLITokenForHost(ctx context.Context, host string) (string, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, ghAuthExecTimeout)
		defer cancel()
	}
	cmd := execCommand(ctx, "glab", "config", "get", "token", "--host", host)
	cmd.Env = append(os.Environ(), "GLAB_CHECK_UPDATE=false", "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		return "", nil
	}
	return singleTokenLine(string(out)), nil
}

// singleTokenLine returns the first non-empty line of out when it is a
// bare token, or "" when the output is empty or not a single token.
func singleTokenLine(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.ContainsFunc(line, func(r rune) bool {
			return r == ' ' || r == '\t'
		}) {
			return ""
		}
		return line
	}
	return ""
}

// forgejoCLIKeysPath returns the fj keys file path. fj uses the Rust
// `directories` crate project data directory for
// ProjectDirs::from("", "forgejo-cli", "forgejo-cli"), which differs per
// platform.
func forgejoCLIKeysPath() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(
			home, "Library", "Application Support",
			"forgejo-cli.forgejo-cli", "keys.json",
		), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", errors.New("APPDATA is not set")
		}
		return filepath.Join(
			appData, "forgejo-cli", "forgejo-cli", "data", "keys.json",
		), nil
	default:
		if dataHome := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dataHome) {
			return filepath.Join(dataHome, "forgejo-cli", "keys.json"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", "forgejo-cli", "keys.json"), nil
	}
}

type forgejoCLIKeys struct {
	Hosts   map[string]forgejoCLILogin `json:"hosts"`
	Aliases map[string]string          `json:"aliases"`
}

type forgejoCLILogin struct {
	Type      string          `json:"type"`
	Token     string          `json:"token"`
	ExpiresAt jsonRawOptional `json:"expires_at"`
}

// jsonRawOptional keeps the raw encoding of a field whose shape depends on
// how fj's Rust `time` crate serialized it.
type jsonRawOptional []byte

func (r *jsonRawOptional) UnmarshalJSON(data []byte) error {
	*r = append((*r)[:0], data...)
	return nil
}

// ForgejoCLITokenForHost returns the token fj stores for host, or "" when
// no keys file exists, the host is not logged in, or its OAuth token has
// expired. Expired OAuth entries are skipped rather than sent: fj refreshes
// them itself the next time it runs, and a rejected stale token would only
// surface as a confusing provider 401. Reading the keys file never errors
// as a missing credential does not; a malformed file is reported so the
// operator learns why the fallback stays silent.
func ForgejoCLITokenForHost(_ context.Context, host string) (string, error) {
	path, err := forgejoCLIKeysPath()
	if err != nil {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	var keys forgejoCLIKeys
	if err := json.Unmarshal(data, &keys); err != nil {
		return "", fmt.Errorf("parse fj keys file %s: %w", path, err)
	}
	login, ok := keys.lookup(host)
	if !ok {
		return "", nil
	}
	if login.expired(time.Now()) {
		return "", nil
	}
	return strings.TrimSpace(login.Token), nil
}

// lookup finds the entry for host: an exact key, an alias fj resolves to a
// key, or the single entry for an instance served under a subpath of host.
func (k forgejoCLIKeys) lookup(host string) (forgejoCLILogin, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return forgejoCLILogin{}, false
	}
	if login, ok := k.Hosts[host]; ok {
		return login, true
	}
	if target, ok := k.Aliases[host]; ok {
		if login, ok := k.Hosts[target]; ok {
			return login, true
		}
	}
	var (
		match forgejoCLILogin
		found int
	)
	for key, login := range k.Hosts {
		if strings.HasPrefix(strings.ToLower(key), host+"/") {
			match = login
			found++
		}
	}
	if found == 1 {
		return match, true
	}
	return forgejoCLILogin{}, false
}

// expired reports whether an OAuth entry's token has lapsed. Application
// tokens never expire. An expiry in an encoding this reader does not
// recognize is treated as live so an unforeseen fj release cannot silently
// disable the fallback.
func (l forgejoCLILogin) expired(now time.Time) bool {
	if !strings.EqualFold(l.Type, "OAuth") || len(l.ExpiresAt) == 0 {
		return false
	}
	expiresAt, ok := parseForgejoCLITime(l.ExpiresAt)
	if !ok {
		return false
	}
	return !now.Before(expiresAt)
}

// parseForgejoCLITime decodes the serde encodings fj's `time` crate may
// use for OffsetDateTime: a human-readable string, or the compact array
// [year, ordinal_day, hour, minute, second, nanosecond, offset_hours,
// offset_minutes, offset_seconds].
func parseForgejoCLITime(raw []byte) (time.Time, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		for _, layout := range []string{
			time.RFC3339Nano,
			"2006-01-02 15:04:05.999999999 -07:00:00",
			"2006-01-02 15:04:05.999999999 -07:00",
		} {
			if t, err := time.Parse(layout, text); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	var parts []int64
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) != 9 {
		return time.Time{}, false
	}
	// The wall-clock fields are in the recorded offset; subtracting that
	// offset yields the UTC instant without building a timezone.
	offset := time.Duration(parts[6])*time.Hour +
		time.Duration(parts[7])*time.Minute +
		time.Duration(parts[8])*time.Second
	t := time.Date(int(parts[0]), time.January, 1, 0, 0, 0, 0, time.UTC)
	t = t.AddDate(0, 0, int(parts[1])-1)
	t = t.Add(
		time.Duration(parts[2])*time.Hour +
			time.Duration(parts[3])*time.Minute +
			time.Duration(parts[4])*time.Second +
			time.Duration(parts[5])*time.Nanosecond,
	)
	return t.Add(-offset), true
}
