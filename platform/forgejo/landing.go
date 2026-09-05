package forgejo

import (
	"context"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"
)

func (c *Client) LandingCapabilities() platform.LandingCapabilities {
	return gitealike.LandingCapabilities()
}
func (c *Client) CollectLandingEvidence(ctx context.Context, ref platform.RepoRef, budget platform.Budget) (platform.LandingSnapshot, error) {
	return gitealike.ReadLandingEvidence(ctx, c.evidenceHTTP, c.baseURL, platform.KindForgejo, c.host, c.clock, ref, budget)
}
