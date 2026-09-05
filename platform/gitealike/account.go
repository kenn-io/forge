package gitealike

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go.kenn.io/forge/platform"
)

// ReadAccount uses the shared user endpoints, not search-by-name inference.
// Both SDKs implement immutable-ID reads through users/search?uid=ID. Account
// type remains unknown: administrator and organization-owner flags are roles.
func ReadAccount(ctx context.Context, client *http.Client, baseURL string, kind platform.Kind, host, login, id string, budget platform.Budget) (platform.Account, error) {
	failure := func(code platform.PlatformErrorCode) error {
		return &platform.Error{Code: code, Provider: kind, PlatformHost: host, Capability: "read_account"}
	}
	meter, err := platform.NewMeter(ctx, budget)
	if err != nil {
		return platform.Account{}, err
	}
	path := "/api/v1/users/" + url.PathEscape(login)
	if id != "" {
		numeric, err := strconv.ParseInt(id, 10, 64)
		if err != nil || numeric <= 0 || strconv.FormatInt(numeric, 10) != id {
			return platform.Account{}, failure(platform.ErrCodeInvalidArgument)
		}
		path = "/api/v1/users/search?uid=" + id
	} else if login == "" || strings.TrimSpace(login) != login || strings.ContainsAny(login, "/\\?#") {
		return platform.Account{}, failure(platform.ErrCodeInvalidArgument)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return platform.Account{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, body, err := meter.ReadHTTP(ctx, client, request)
	if err != nil {
		return platform.Account{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return platform.Account{}, mapTransportError(kind, host, &HTTPError{StatusCode: response.StatusCode})
	}
	if id != "" {
		var envelope struct {
			OK   bool            `json:"ok"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil || !envelope.OK {
			return platform.Account{}, failure(platform.ErrCodeProviderContract)
		}
		decoder := json.NewDecoder(bytes.NewReader(envelope.Data))
		token, err := decoder.Token()
		if err != nil || token != json.Delim('[') {
			return platform.Account{}, failure(platform.ErrCodeProviderContract)
		}
		if !decoder.More() {
			return platform.Account{}, failure(platform.ErrCodeNotFound)
		}
		if err := meter.Records(1); err != nil {
			return platform.Account{}, err
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || decoder.More() {
			return platform.Account{}, failure(platform.ErrCodeProviderContract)
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return platform.Account{}, failure(platform.ErrCodeProviderContract)
		}
		if _, err := decoder.Token(); err != io.EOF {
			return platform.Account{}, failure(platform.ErrCodeProviderContract)
		}
		body = raw
	} else if err := meter.Records(1); err != nil {
		return platform.Account{}, err
	}
	var user struct {
		ID       int64  `json:"id"`
		Login    string `json:"login"`
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(body, &user); err != nil || user.ID <= 0 {
		return platform.Account{}, failure(platform.ErrCodeProviderContract)
	}
	observedID := strconv.FormatInt(user.ID, 10)
	if (id != "" && observedID != id) || (login != "" && !strings.EqualFold(user.Login, login)) {
		return platform.Account{}, failure(platform.ErrCodeProviderContract)
	}
	return platform.AccountResult(platform.Account{ID: observedID, Login: user.Login, DisplayName: user.FullName, Type: platform.AccountUnknown}, meter)
}
