package fleetsetup

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const serviceLabel = "io.kenn.forge"

type localServicePlan struct {
	Kind  string
	Path  string
	Label string
}

func (p localServicePlan) requiredCommands() []string {
	if p.Kind == "launchd LaunchAgent" {
		return []string{"launchctl"}
	}
	return []string{"systemctl", "loginctl"}
}

func servicePlan(goos, home string, _ int) localServicePlan {
	if goos == "darwin" {
		return localServicePlan{
			Kind:  "launchd LaunchAgent",
			Path:  filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"),
			Label: serviceLabel,
		}
	}
	return localServicePlan{
		Kind:  "systemd user service",
		Path:  filepath.Join(home, ".config", "systemd", "user", "kenn-forge.service"),
		Label: "kenn-forge.service",
	}
}

func renderService(plan Plan) ([]byte, os.FileMode, error) {
	if plan.ServiceKind == "launchd LaunchAgent" {
		return renderLaunchAgent(plan), 0o600, nil
	}
	if plan.ServiceKind == "systemd user service" {
		return renderSystemdUserService(plan), 0o600, nil
	}
	return nil, 0, fmt.Errorf("unsupported service kind %q", plan.ServiceKind)
}

func renderSystemdUserService(plan Plan) []byte {
	escape := func(value string) string {
		value = strings.ReplaceAll(value, "\\", "\\\\")
		value = strings.ReplaceAll(value, "\"", "\\\"")
		value = strings.ReplaceAll(value, "%", "%%")
		return "\"" + value + "\""
	}
	return []byte(fmt.Sprintf(`[Unit]
Description=Kenn Forge
After=network-online.target
Wants=network-online.target

[Service]
# %s
Type=simple
KillMode=process
Environment=HOME=%s
Environment=PATH=%s
Environment=KENN_FORGE_DEV_RESTART=1
ExecStart=%s serve --config %s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, managedFileMarker, escape(plan.HomeDir), escape(plan.PathEnv),
		escape(plan.BinaryPath), escape(plan.ConfigPath)))
}

func renderLaunchAgent(plan Plan) []byte {
	// launchd property lists have a tiny fixed schema here. Escaping every
	// operator-controlled value through xmlEscape keeps arguments as values,
	// never shell text.
	logDir := filepath.Join(plan.DataDir, "logs")
	values := func(items ...string) string {
		var builder strings.Builder
		for _, item := range items {
			fmt.Fprintf(&builder, "    <string>%s</string>\n", xmlEscape(item))
		}
		return builder.String()
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- %s -->
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key><string>%s</string>
    <key>PATH</key><string>%s</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, managedFileMarker, serviceLabel,
		values(plan.BinaryPath, "serve", "--config", plan.ConfigPath),
		xmlEscape(plan.HomeDir), xmlEscape(plan.PathEnv),
		xmlEscape(filepath.Join(logDir, "service.stdout.log")),
		xmlEscape(filepath.Join(logDir, "service.stderr.log"))))
}

func xmlEscape(value string) string {
	var builder strings.Builder
	_ = xml.EscapeText(&builder, []byte(value))
	return builder.String()
}

func (r *Runner) applyService(ctx context.Context, plan Plan, transaction *transaction) error {
	previous, err := r.deps.readFile(plan.ServicePath)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("snapshot service file: %w", err)
	}
	content, mode, err := renderService(plan)
	if err != nil {
		return err
	}
	if plan.ServiceKind == "launchd LaunchAgent" {
		if err := os.MkdirAll(filepath.Join(plan.DataDir, "logs"), 0o700); err != nil {
			return fmt.Errorf("create service log directory: %w", err)
		}
	}
	if err := r.deps.writeFile(plan.ServicePath, content, mode); err != nil {
		return fmt.Errorf("write service file: %w", err)
	}
	transaction.record(func(ctx context.Context) error {
		var stopErr error
		if plan.ServiceKind == "systemd user service" {
			_, stopErr = r.deps.run(ctx, "systemctl", "--user", "disable", "--now", plan.ServiceLabel)
		} else {
			target := fmt.Sprintf("gui/%d/%s", plan.UID, plan.ServiceLabel)
			_, stopErr = r.deps.run(ctx, "launchctl", "bootout", target)
		}
		var restoreErr error
		if existed {
			restoreErr = r.deps.writeFile(plan.ServicePath, previous, mode)
		} else if err := r.deps.remove(plan.ServicePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			restoreErr = err
		}
		var reloadErr error
		if plan.ServiceKind == "systemd user service" {
			_, reloadErr = r.deps.run(ctx, "systemctl", "--user", "daemon-reload")
			if existed && restoreErr == nil {
				_, reloadErr = r.deps.run(ctx, "systemctl", "--user", "enable", "--now", plan.ServiceLabel)
			}
		} else if existed && restoreErr == nil {
			domain := fmt.Sprintf("gui/%d", plan.UID)
			_, reloadErr = r.deps.run(ctx, "launchctl", "bootstrap", domain, plan.ServicePath)
		}
		return errors.Join(stopErr, restoreErr, reloadErr)
	})
	if plan.ServiceKind == "systemd user service" {
		if _, err := r.deps.run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("reload systemd user manager: %w", err)
		}
	}
	return nil
}

func (r *Runner) restartService(ctx context.Context, plan Plan) error {
	if plan.ServiceKind == "systemd user service" {
		if _, err := r.deps.run(
			ctx, "systemctl", "--user", "enable", "--now", plan.ServiceLabel,
		); err != nil {
			return fmt.Errorf("start systemd user service: %w", err)
		}
		if _, err := r.deps.run(
			ctx, "systemctl", "--user", "restart", plan.ServiceLabel,
		); err != nil {
			return fmt.Errorf("restart systemd user service: %w", err)
		}
		return nil
	}
	domain := fmt.Sprintf("gui/%d", plan.UID)
	target := domain + "/" + plan.ServiceLabel
	_, _ = r.deps.run(ctx, "launchctl", "bootout", target)
	if _, err := r.deps.run(ctx, "launchctl", "bootstrap", domain, plan.ServicePath); err != nil {
		return fmt.Errorf("load LaunchAgent: %w", err)
	}
	if _, err := r.deps.run(ctx, "launchctl", "kickstart", "-k", target); err != nil {
		return fmt.Errorf("restart LaunchAgent: %w", err)
	}
	return nil
}
