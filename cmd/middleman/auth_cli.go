package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/runtimelock"
	"go.kenn.io/middleman/internal/server"
)

// runAuthCLI implements `middleman auth <url|token|rotate>`: it surfaces
// the minted API token for the browser/CLI login flow and rotates it.
func runAuthCLI(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: middleman auth <url|token|rotate>")
	}
	switch args[0] {
	case "url":
		return runAuthURL(args[1:], stdout)
	case "token":
		return runAuthToken(args[1:], stdout)
	case "rotate":
		return runAuthRotate(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown auth subcommand %q; use url, token, or rotate", args[0])
	}
}

func runAuthToken(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("middleman auth token", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return authTokenOutput(cfg.DataDir, stdout)
}

func runAuthURL(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("middleman auth url", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	baseURL := fs.String("base-url", "", "externally reachable base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return authURLOutput(cfg.DataDir, *baseURL, stdout)
}

func runAuthRotate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("middleman auth rotate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	force := fs.Bool("force", false, "rotate even if a daemon is running")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return authRotateOutput(cfg.DataDir, *force, stdout, stderr)
}

func authTokenOutput(dataDir string, stdout io.Writer) error {
	token, err := runtimelock.ReadAuthToken(dataDir)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("no auth token under %s; start the daemon once to mint it", dataDir)
	}
	_, err = fmt.Fprintln(stdout, token)
	return err
}

func authURLOutput(dataDir, baseURLFlag string, stdout io.Writer) error {
	token, err := runtimelock.ReadAuthToken(dataDir)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("no auth token under %s; start the daemon once to mint it", dataDir)
	}
	base := strings.TrimSuffix(baseURLFlag, "/")
	if base == "" {
		st, err := runtimelock.Read(dataDir)
		if err != nil {
			return fmt.Errorf("read runtime status: %w", err)
		}
		if !st.Running || st.Metadata == nil {
			return errors.New("no running daemon to infer the URL from; pass --base-url <url>")
		}
		base = defaultBaseURL(st.Metadata)
	}
	_, err = fmt.Fprintln(stdout, server.AuthBootstrapURL(base, token))
	return err
}

// authRotateOutput rotates the token while holding the runtime lock, so
// a daemon cannot start (and cache the old token) between the running
// check and the rotation. On a lock collision the daemon is running:
// refuse unless forced, because the live daemon keeps honoring the old
// token until restarted.
func authRotateOutput(dataDir string, force bool, stdout, stderr io.Writer) error {
	handle, err := runtimelock.Acquire(dataDir)
	var collision *runtimelock.CollisionError
	switch {
	case err == nil:
		defer func() { _ = handle.Release() }()
	case errors.As(err, &collision):
		if !force {
			return errors.New(
				"a middleman daemon is running; rotating now would make CLI clients send the" +
					" new token while the server still accepts only the old one. Stop the daemon" +
					" first, or pass --force to rotate anyway and restart the daemon afterward")
		}
	default:
		return fmt.Errorf("acquire runtime lock: %w", err)
	}
	token, err := runtimelock.RotateAuthToken(dataDir)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, token); err != nil {
		return err
	}
	if collision != nil {
		_, err = fmt.Fprintln(stderr,
			"WARNING: the running daemon still honors the OLD token until restarted;"+
				" clients using the new token will get 401 until you restart it.")
		return err
	}
	_, err = fmt.Fprintln(stderr, "Rotated. Restart the daemon to apply.")
	return err
}

// defaultBaseURL builds the on-host base URL from runtime metadata,
// normalizing the listener host: unspecified binds (0.0.0.0, ::) become
// loopback and IPv6 literals are bracketed, so the URL is browser-usable.
func defaultBaseURL(meta *runtimelock.Metadata) string {
	host := meta.Host
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsUnspecified():
			host = "127.0.0.1"
		case ip.To4() == nil:
			host = "[" + host + "]"
		}
	}
	return fmt.Sprintf("http://%s:%d%s", host, meta.Port, strings.TrimSuffix(meta.BasePath, "/"))
}
