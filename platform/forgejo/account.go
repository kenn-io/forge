package forgejo

import (
	"context"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"
)

func (c *Client) LookupAccount(ctx context.Context, login string, budget platform.Budget) (platform.Account, error) {
	return gitealike.ReadAccount(ctx, c.transport.httpClient, c.baseURL, platform.KindForgejo, c.host, login, "", budget)
}
func (c *Client) GetAccount(ctx context.Context, id string, budget platform.Budget) (platform.Account, error) {
	return gitealike.ReadAccount(ctx, c.transport.httpClient, c.baseURL, platform.KindForgejo, c.host, "", id, budget)
}
