package githubappcli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/internal/config"
)

func newInstallCommand(env *appEnv) *cobra.Command {
	var sel appSelector
	var noBrowser bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Open the install page for an app and record the installation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWithInterrupt(cmd, func(ctx context.Context) error {
				return runInstall(ctx, sel, noBrowser, timeout, env)
			})
		},
	}
	registerConfigFlag(cmd, env)
	sel.register(cmd, "install")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print URLs instead of opening a browser")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "how long to wait for the installation")
	registerTestFlags(cmd, env)
	return cmd
}

func runInstall(
	ctx context.Context, sel appSelector, noBrowser bool, timeout time.Duration, env *appEnv,
) error {
	cfg, err := env.loadConfig()
	if err != nil {
		return err
	}
	app, err := sel.selectApp(cfg)
	if err != nil {
		return err
	}
	return env.runInstallFlow(ctx, cfg, app, !noBrowser, timeout)
}

func newUninstallCommand(env *appEnv) *cobra.Command {
	var sel appSelector
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall an app from its account (keeps the app)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWithInterrupt(cmd, func(ctx context.Context) error {
				return runUninstall(ctx, sel, yes, env)
			})
		},
	}
	registerConfigFlag(cmd, env)
	sel.register(cmd, "uninstall")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm uninstalling without prompting")
	registerTestFlags(cmd, env)
	return cmd
}

func runUninstall(ctx context.Context, sel appSelector, yes bool, env *appEnv) error {
	cfg, err := env.loadConfig()
	if err != nil {
		return err
	}
	app, err := sel.selectApp(cfg)
	if err != nil {
		return err
	}
	if app.InstallationID == 0 {
		return fmt.Errorf("app %q has no recorded installation", app.Slug)
	}
	if !yes {
		return fmt.Errorf(
			"uninstalling removes the app's access to %s; re-run with --yes to confirm",
			app.InstallationAccount,
		)
	}
	jwt, err := appJWT(app, env.now())
	if err != nil {
		return err
	}
	err = env.apiClient(app.Host).DeleteInstallation(ctx, jwt, app.InstallationID)
	// A 404 means the installation is already gone on GitHub's side;
	// still clear the stale local record.
	if err != nil && !githubapp.IsStatus(err, http.StatusNotFound) {
		return err
	}
	fmt.Fprintf(env.stdout,
		"Uninstalled app %q from %s. kenn-forge falls back to PAT tokens for %s.\n",
		app.Slug, app.InstallationAccount, app.Host,
	)
	oldApp := app
	app.InstallationID = 0
	app.InstallationAccount = ""
	return updateAppSlotInConfig(cfg, env.configPath, oldApp, app)
}

type deleteOptions struct {
	sel       appSelector
	yes       bool
	localOnly bool
	noBrowser bool
	timeout   time.Duration
}

func newDeleteCommand(env *appEnv) *cobra.Command {
	var opts deleteOptions
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete an app (opens browser; removes local credentials)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWithInterrupt(cmd, func(ctx context.Context) error {
				return runDelete(ctx, opts, env)
			})
		},
	}
	flags := cmd.Flags()
	registerConfigFlag(cmd, env)
	opts.sel.register(cmd, "delete")
	flags.BoolVar(&opts.yes, "yes", false, "confirm deletion without prompting")
	flags.BoolVar(&opts.localOnly, "local-only", false,
		"only remove the local config entry and key (app already deleted on GitHub)")
	flags.BoolVar(&opts.noBrowser, "no-browser", false, "print URLs instead of opening a browser")
	flags.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "how long to wait for the deletion")
	registerTestFlags(cmd, env)
	return cmd
}

func runDelete(ctx context.Context, opts deleteOptions, env *appEnv) error {
	cfg, err := env.loadConfig()
	if err != nil {
		return err
	}
	app, err := opts.sel.selectApp(cfg)
	if err != nil {
		return err
	}
	if !opts.yes {
		return fmt.Errorf(
			"deleting app %q removes its credentials permanently; re-run with --yes to confirm",
			app.Slug,
		)
	}
	configuredApp := app

	if !opts.localOnly {
		// Deletion is confirmed by the app's credentials dying, so the
		// credentials must demonstrably work first. Otherwise a stale
		// or rotated key would read as "deleted" and local state would
		// be removed while the app keeps its access on GitHub.
		client := env.apiClient(app.Host)
		app, err = env.refreshAppMetadata(ctx, client, app)
		if err != nil {
			if githubapp.IsStatus(err, http.StatusUnauthorized) ||
				githubapp.IsStatus(err, http.StatusNotFound) {
				return fmt.Errorf(
					"cannot verify deletion, GitHub rejected the app credentials: %w; "+
						"if the app is already deleted, re-run with --local-only", err,
				)
			}
			return fmt.Errorf(
				"cannot verify deletion, app credentials are unusable: %w; "+
					"delete the app in GitHub settings yourself, then re-run with --local-only", err,
			)
		}

		// GitHub has no API for deleting an app; the user confirms it
		// in browser settings while we poll for the credentials to die.
		url := settingsURL(env.webBaseFor(app.Host), app) + "/advanced"
		fmt.Fprintf(env.stdout,
			"Delete the app in GitHub settings (Danger Zone -> Delete GitHub App):\n  %s\n", url,
		)
		if !opts.noBrowser {
			if err := env.openBrowser(url); err != nil {
				fmt.Fprintf(env.stdout, "could not open browser: %v\n", err)
			}
		}
		fmt.Fprintln(env.stdout, "Waiting for the app to disappear...")
		err = env.pollUntil(ctx, opts.timeout, func(ctx context.Context) (bool, error) {
			jwt, err := appJWT(app, env.now())
			if err != nil {
				return false, err
			}
			_, err = client.GetApp(ctx, jwt)
			if err == nil {
				return false, nil
			}
			if githubapp.IsStatus(err, http.StatusUnauthorized) ||
				githubapp.IsStatus(err, http.StatusNotFound) {
				return true, nil
			}
			return false, err
		})
		if err != nil {
			return fmt.Errorf(
				"app still exists on %s: %w; "+
					"re-run with --local-only after deleting it in the browser", app.Host, err,
			)
		}
	}

	if err := removeAppFromConfig(cfg, env.configPath, app); err != nil {
		return err
	}
	if appPrivateKeyOwnedByCLI(env.configPath, configuredApp, app.Slug) {
		if err := os.Remove(app.PrivateKeyPath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(env.stdout, "could not remove private key %s: %v\n", app.PrivateKeyPath, err)
		} else {
			fmt.Fprintf(env.stdout, "Removed private key %s\n", app.PrivateKeyPath)
		}
	} else {
		fmt.Fprintf(env.stdout, "Preserved external private key %s\n", app.PrivateKeyPath)
	}
	fmt.Fprintf(env.stdout, "Deleted app %q for host %s from config.\n", app.Slug, app.Host)
	return nil
}

func appPrivateKeyOwnedByCLI(
	configPath string, app config.GitHubAppConfig, alternateSlugs ...string,
) bool {
	configDir, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return false
	}
	keyPath, err := filepath.Abs(app.PrivateKeyPath)
	if err != nil {
		return false
	}
	if filepath.Dir(keyPath) != configDir {
		return false
	}
	base := filepath.Base(keyPath)
	prefix := "github-app-" + strings.ReplaceAll(app.Host, ":", "_") + "-"
	slugs := append([]string{app.Slug}, alternateSlugs...)
	for _, slug := range slugs {
		if slug == "" {
			continue
		}
		if base == prefix+slug+".pem" {
			return true
		}
	}
	return false
}

func newOpenCommand(env *appEnv) *cobra.Command {
	var sel appSelector
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Open an app's GitHub settings page",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWithInterrupt(cmd, func(ctx context.Context) error {
				return runOpen(ctx, sel, env)
			})
		},
	}
	registerConfigFlag(cmd, env)
	sel.register(cmd, "open")
	registerTestFlags(cmd, env)
	return cmd
}

func runOpen(ctx context.Context, sel appSelector, env *appEnv) error {
	cfg, err := env.loadConfig()
	if err != nil {
		return err
	}
	app, err := sel.selectApp(cfg)
	if err != nil {
		return err
	}
	client := env.apiClient(app.Host)
	app, err = env.refreshAppMetadata(ctx, client, app)
	if err != nil {
		return err
	}
	url := settingsURL(env.webBaseFor(app.Host), app)
	fmt.Fprintf(env.stdout, "App settings: %s\n", url)
	return env.openBrowser(url)
}
