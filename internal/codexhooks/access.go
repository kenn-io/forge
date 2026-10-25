package codexhooks

import (
	"cmp"
	"context"
	"errors"
	"maps"
)

// EffectiveAccess asks Codex to resolve the workspace's sandbox and approval policy.
func EffectiveAccess(ctx context.Context, command, env []string, cwd string, config map[string]any) (sandbox, approval string, err error) {
	c, closeClient, err := startClient(ctx, command[0], command[1:], env, cwd)
	if err != nil {
		return "", "", err
	}
	defer closeClient()
	return c.readAccess(cwd, config)
}

func (c *client) readAccess(cwd string, config map[string]any) (sandbox, approval string, err error) {
	config = maps.Clone(config)
	delete(config, "hooks")
	if config == nil {
		config = make(map[string]any)
	}
	trustedConfig := maps.Clone(config)
	// Match codex-acp's session trust override so project permissions are loaded.
	trustedConfig["projects"] = map[string]any{cwd: map[string]any{"trust_level": "trusted"}}
	sandbox, approval, err = c.threadAccess(trustedConfig)
	if err != nil || sandbox != "dangerFullAccess" {
		return sandbox, approval, err
	}
	// Untrusted project config may lower access but must not grant Full access.
	untrustedSandbox, untrustedApproval, err := c.threadAccess(config)
	if err != nil {
		return "", "", err
	}
	if sandbox != untrustedSandbox || approval != untrustedApproval {
		return "", "", errors.New("full access comes from project config Codex doesn't trust")
	}
	return sandbox, approval, nil
}

func (c *client) threadAccess(config map[string]any) (sandbox, approval string, err error) {
	params := map[string]any{"ephemeral": true, "config": config}
	var result struct {
		Sandbox struct {
			Type string `json:"type"`
		} `json:"sandbox"`
		ApprovalPolicy any `json:"approvalPolicy"`
	}
	// Omitting cwd avoids persisting a project trust entry in the user's config.
	if err := c.call("thread/start", params, &result); err != nil || result.Sandbox.Type == "" {
		return "", "", cmp.Or(err, errors.New("thread/start reply has no sandbox type"))
	}
	approval, _ = result.ApprovalPolicy.(string)
	return result.Sandbox.Type, approval, nil
}
