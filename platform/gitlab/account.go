package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	"go.kenn.io/forge/platform"
)

// GitLab's SDK User.Bot is a bool. Decode this endpoint into a presence-aware
// shape so omission cannot become evidence of a plain user account.
type accountResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Bot      *bool  `json:"bot"`
}

func (c *Client) LookupAccount(ctx context.Context, login string, budget platform.Budget) (platform.Account, error) {
	if login == "" || strings.TrimSpace(login) != login {
		return platform.Account{}, c.accountError(platform.ErrCodeInvalidArgument)
	}
	meter, err := platform.NewMeter(ctx, budget)
	if err != nil {
		return platform.Account{}, err
	}
	body, response, err := c.accountBody(ctx, "users", &gitlab.ListUsersOptions{Username: &login}, meter)
	if err != nil {
		return platform.Account{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return platform.Account{}, c.accountError(platform.ErrCodeProviderContract)
	}
	if !decoder.More() {
		return platform.Account{}, c.accountError(platform.ErrCodeNotFound)
	}
	if err := meter.Records(1); err != nil {
		return platform.Account{}, err
	}
	var user accountResponse
	if err := decoder.Decode(&user); err != nil {
		return platform.Account{}, c.accountError(platform.ErrCodeProviderContract)
	}
	if decoder.More() || user.ID <= 0 || user.Username != login || response.Header.Get("X-Next-Page") != "" {
		return platform.Account{}, c.accountError(platform.ErrCodeProviderContract)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
		return platform.Account{}, c.accountError(platform.ErrCodeProviderContract)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return platform.Account{}, c.accountError(platform.ErrCodeProviderContract)
	}
	account, err := c.getAccount(ctx, strconv.FormatInt(user.ID, 10), meter)
	if err != nil {
		return platform.Account{}, err
	}
	if account.Login != login {
		return platform.Account{}, c.accountError(platform.ErrCodeStaleState)
	}
	return account, nil
}

func (c *Client) GetAccount(ctx context.Context, id string, budget platform.Budget) (platform.Account, error) {
	meter, err := platform.NewMeter(ctx, budget)
	if err != nil {
		return platform.Account{}, err
	}
	return c.getAccount(ctx, id, meter)
}

func (c *Client) getAccount(ctx context.Context, id string, meter *platform.Meter) (platform.Account, error) {
	numeric, err := strconv.ParseInt(id, 10, 64)
	if err != nil || numeric <= 0 || strconv.FormatInt(numeric, 10) != id {
		return platform.Account{}, c.accountError(platform.ErrCodeInvalidArgument)
	}
	if err := meter.Records(1); err != nil {
		return platform.Account{}, err
	}
	body, _, err := c.accountBody(ctx, "users/"+id, nil, meter)
	if err != nil {
		return platform.Account{}, err
	}
	var user accountResponse
	if err := json.Unmarshal(body, &user); err != nil {
		return platform.Account{}, c.accountError(platform.ErrCodeProviderContract)
	}
	if user.ID != numeric {
		return platform.Account{}, c.accountError(platform.ErrCodeProviderContract)
	}
	kind := platform.AccountUnknown
	if user.Bot != nil {
		kind = platform.AccountUser
		if *user.Bot {
			kind = platform.AccountBot
		}
	}
	return platform.AccountResult(platform.Account{ID: id, Login: user.Username, DisplayName: user.Name, Type: kind}, meter)
}

func (c *Client) accountBody(ctx context.Context, path string, options any, meter *platform.Meter) ([]byte, *http.Response, error) {
	req, err := c.api.NewRequest(http.MethodGet, path, options, []gitlab.RequestOptionFunc{gitlab.WithContext(ctx)})
	if err != nil {
		return nil, nil, err
	}
	response, body, err := meter.ReadHTTP(ctx, c.httpClient, req.Request)
	if err != nil {
		return nil, nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	if err := gitlab.CheckResponse(response); err != nil {
		return nil, response, c.mapGitLabError("read_account", err)
	}
	return body, response, nil
}

func (c *Client) accountError(code platform.PlatformErrorCode) error {
	return &platform.Error{Code: code, Provider: platform.KindGitLab, PlatformHost: c.host, Capability: "read_account"}
}
