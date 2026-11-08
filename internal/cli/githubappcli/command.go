// Package githubappcli implements "kenn-forge github-app", which creates
// and manages GitHub Apps whose installation tokens kenn-forge uses
// instead of personal access tokens. Installation tokens carry their
// own rate-limit budget (5,000+ requests/hour, scaling with repository
// count), so a dedicated app takes sync traffic off the PAT that
// interactive tools share.
//
// App creation uses GitHub's App Manifest flow: the command serves a
// one-page form on loopback, opens the browser, and the only manual
// step is clicking "Create GitHub App" (and later "Install"). The
// resulting app id, private key, and installation are written to
// kenn-forge's config.toml, where the sync engine picks them up.
package githubappcli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"go.kenn.io/forge/internal/config"
)

// CommandName is the kenn-forge subcommand that manages GitHub Apps.
const CommandName = "github-app"

// NewCommand returns the github-app command tree writing to stdout.
func NewCommand(stdout io.Writer) *cobra.Command {
	return newCommand(defaultEnv(stdout))
}

func newCommand(env *appEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:           CommandName,
		Short:         "Manage GitHub Apps for kenn-forge's sync engine",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.AddCommand(
		newCreateCommand(env),
		newListCommand(env),
		newInstallCommand(env),
		newUninstallCommand(env),
		newDeleteCommand(env),
		newOpenCommand(env),
	)
	return cmd
}

// runWithInterrupt cancels run's context on Ctrl-C so the browser
// flows stop waiting and close their loopback server.
func runWithInterrupt(cmd *cobra.Command, run func(context.Context) error) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()
	return run(ctx)
}

// appEnv carries the CLI's effectful dependencies so tests can run
// commands in-process against a fake GitHub with a scripted browser.
type appEnv struct {
	stdout       io.Writer
	openBrowser  func(url string) error
	configPath   string
	apiBase      string // override; empty derives from --host
	webBase      string // override; empty derives from --host
	pollInterval time.Duration
	now          func() time.Time
}

func defaultEnv(stdout io.Writer) *appEnv {
	return &appEnv{
		stdout:       stdout,
		openBrowser:  openInBrowser,
		configPath:   config.DefaultConfigPath(),
		pollInterval: 2 * time.Second,
		now:          time.Now,
	}
}
