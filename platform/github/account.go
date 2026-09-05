package github

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v89/github"
	"go.kenn.io/forge/platform"
)

func (c *Client) LookupAccount(ctx context.Context, login string, budget platform.Budget) (platform.Account, error) {
	if login == "" || strings.TrimSpace(login) != login || strings.ContainsAny(login, "/\\?#") {
		return platform.Account{}, c.accountError(platform.ErrCodeInvalidArgument)
	}
	return c.readAccount(ctx, "users/"+url.PathEscape(login), "", login, budget)
}

func (c *Client) GetAccount(ctx context.Context, id string, budget platform.Budget) (platform.Account, error) {
	numeric, err := strconv.ParseInt(id, 10, 64)
	if err != nil || numeric <= 0 || strconv.FormatInt(numeric, 10) != id {
		return platform.Account{}, c.accountError(platform.ErrCodeInvalidArgument)
	}
	return c.readAccount(ctx, "user/"+id, id, "", budget)
}

func (c *Client) readAccount(ctx context.Context, path, id, login string, budget platform.Budget) (platform.Account, error) {
	meter, err := platform.NewMeter(ctx, budget)
	if err != nil {
		return platform.Account{}, err
	}
	if err := meter.Records(1); err != nil {
		return platform.Account{}, err
	}
	request, err := c.gh.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return platform.Account{}, err
	}
	response, body, err := meter.ReadHTTP(c.authContext(ctx, "", false), c.httpClient, request)
	if err != nil {
		return platform.Account{}, err
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	if err := gh.CheckResponse(response); err != nil {
		return platform.Account{}, mapGitHubReadError(c.platformHost, c.now, "read_account", err)
	}
	var user gh.User
	if err := json.Unmarshal(body, &user); err != nil {
		return platform.Account{}, c.accountError(platform.ErrCodeProviderContract)
	}
	account := NormalizeAccount(&user)
	if account == nil || (id != "" && account.ID != id) || (login != "" && !strings.EqualFold(account.Login, login)) {
		return platform.Account{}, c.accountError(platform.ErrCodeProviderContract)
	}
	return platform.AccountResult(*account, meter)
}

func (c *Client) accountError(code platform.PlatformErrorCode) error {
	return &platform.Error{Code: code, Provider: platform.KindGitHub, PlatformHost: c.platformHost, Capability: "read_account"}
}

func (p *Provider) LookupAccount(ctx context.Context, login string, budget platform.Budget) (platform.Account, error) {
	reader, ok := p.client.(platform.AccountReader)
	if !ok {
		return platform.Account{}, platform.UnsupportedCapability(platform.KindGitHub, p.host, "read_account")
	}
	account, err := reader.LookupAccount(ctx, login, budget)
	return account, p.archiveTransportError("read_account", err)
}

func (p *Provider) GetAccount(ctx context.Context, id string, budget platform.Budget) (platform.Account, error) {
	reader, ok := p.client.(platform.AccountReader)
	if !ok {
		return platform.Account{}, platform.UnsupportedCapability(platform.KindGitHub, p.host, "read_account")
	}
	account, err := reader.GetAccount(ctx, id, budget)
	return account, p.archiveTransportError("read_account", err)
}

// NormalizeAccount preserves absent accounts and explicitly reported types.
// A display name or login suffix is not account-type evidence.
func NormalizeAccount(user *gh.User) *platform.Account {
	if user == nil || user.GetID() <= 0 {
		return nil
	}
	kind := platform.AccountUnknown
	switch user.GetType() {
	case "User":
		kind = platform.AccountUser
	case "Bot":
		kind = platform.AccountBot
	case "Organization":
		kind = platform.AccountOrganization
	}
	return &platform.Account{
		ID: strconv.FormatInt(user.GetID(), 10), Login: user.GetLogin(),
		DisplayName: user.GetName(), Type: kind,
	}
}
