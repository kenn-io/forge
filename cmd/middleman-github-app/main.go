// Command middleman-github-app creates and manages GitHub Apps whose
// installation tokens middleman uses instead of personal access
// tokens. Installation tokens carry their own rate-limit budget
// (5,000+ requests/hour, scaling with repository count), so a
// dedicated app takes sync traffic off the PAT that interactive tools
// share.
//
// App creation uses GitHub's App Manifest flow: the tool serves a
// one-page form on loopback, opens the browser, and the only manual
// step is clicking "Create GitHub App" (and later "Install"). The
// resulting app id, private key, and installation are written to
// middleman's config.toml, where the sync engine picks them up.
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"go.kenn.io/middleman/internal/config"
)

const usage = `middleman-github-app manages GitHub Apps for middleman's sync engine.

Usage:
  middleman-github-app <command> [flags]

Commands:
  create      Create a GitHub App via the browser manifest flow and
              write its credentials to middleman's config
  list        List configured apps with installation and rate-limit state
  install     Open the install page for an app and record the installation
  uninstall   Uninstall an app from its account (keeps the app)
  delete      Delete an app (opens browser; removes local credentials)
  open        Open an app's GitHub settings page

Run "middleman-github-app <command> -h" for command flags.
`

func main() {
	if err := runCLI(os.Args[1:], defaultEnv(os.Stdout)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
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

func runCLI(args []string, env *appEnv) error {
	if len(args) == 0 {
		fmt.Fprint(env.stdout, usage)
		return fmt.Errorf("a command is required")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "create":
		return runCreate(rest, env)
	case "list":
		return runList(rest, env)
	case "install":
		return runInstall(rest, env)
	case "uninstall":
		return runUninstall(rest, env)
	case "delete":
		return runDelete(rest, env)
	case "open":
		return runOpen(rest, env)
	case "help", "-h", "--help":
		fmt.Fprint(env.stdout, usage)
		return nil
	default:
		fmt.Fprint(env.stdout, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}
