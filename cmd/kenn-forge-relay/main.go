package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/pflag"
	"go.kenn.io/forge/internal/activityrelay"
)

var version = "dev"

type relayConfig struct {
	WebhookListen string `toml:"webhook_listen"`
	FeedListen    string `toml:"feed_listen"`
	Sources       map[string]struct {
		SecretFile    string  `toml:"secret_file"`
		RepositoryIDs []int64 `toml:"repository_ids"`
	} `toml:"sources"`
}

func main() {
	configFile := pflag.String("config", "", "Path to relay TOML configuration")
	showVersion := pflag.Bool("version", false, "Print build version")
	pflag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if pflag.NArg() != 0 || *configFile == "" {
		fmt.Fprintln(os.Stderr, "usage: kenn-forge-relay --config PATH")
		os.Exit(2)
	}
	if err := run(ctx, *configFile, os.Stdout); err != nil {
		slog.Error("relay stopped", "error", err)
		os.Exit(1)
	}
}

// run serves until ctx ends and writes the bound listener addresses to ready
// as one JSON line once both servers accept connections.
func run(ctx context.Context, path string, ready io.Writer) error {
	cfg := relayConfig{WebhookListen: "127.0.0.1:8081", FeedListen: "127.0.0.1:8082"}
	metadata, err := toml.DecodeFile(path, &cfg)
	if err != nil || len(metadata.Undecoded()) != 0 || len(cfg.Sources) == 0 {
		return errors.New("invalid relay configuration")
	}
	for _, address := range []string{cfg.WebhookListen, cfg.FeedListen} {
		host, _, err := net.SplitHostPort(address)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("relay listeners must use loopback IP addresses")
		}
	}
	sources := make(map[string]activityrelay.Source, len(cfg.Sources))
	validSource := regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	for name, source := range cfg.Sources {
		if !validSource.MatchString(name) || len(source.RepositoryIDs) == 0 {
			return errors.New("source requires a routing label and repository IDs")
		}
		for _, id := range source.RepositoryIDs {
			if id <= 0 {
				return errors.New("source repository IDs must be positive")
			}
		}
		secret, err := os.ReadFile(source.SecretFile)
		if err != nil || len(bytes.TrimSpace(secret)) == 0 {
			return errors.New("could not read source verification credential")
		}
		sources[name] = activityrelay.Source{Secret: bytes.TrimSpace(secret), RepositoryIDs: source.RepositoryIDs}
	}
	webhook, err := net.Listen("tcp", cfg.WebhookListen)
	if err != nil {
		return errors.New("could not bind webhook listener")
	}
	defer func() { _ = webhook.Close() }()
	feed, err := net.Listen("tcp", cfg.FeedListen)
	if err != nil {
		return errors.New("could not bind feed listener")
	}
	defer func() { _ = feed.Close() }()
	ingress, private := activityrelay.Handlers(new(activityrelay.Broadcaster), sources)
	// Subscriptions outlive any request timeout, so graceful shutdown must end
	// them explicitly; http.Server.Shutdown only waits for handlers to return.
	streamCtx, stopStreams := context.WithCancel(ctx)
	defer stopStreams()
	servers := []*http.Server{
		{Handler: ingress, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10},
		{
			Handler: private, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10,
			BaseContext: func(net.Listener) context.Context { return streamCtx },
		},
	}
	finished := make(chan error, 2)
	go func() { finished <- servers[0].Serve(webhook) }()
	go func() { finished <- servers[1].Serve(feed) }()
	addresses, _ := json.Marshal(map[string]string{"webhook": webhook.Addr().String(), "feed": feed.Addr().String()})
	fmt.Fprintln(ready, string(addresses))
	var serveErr error
	completed := 0
	select {
	case <-ctx.Done():
	case err := <-finished:
		completed++
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = errors.New("relay HTTP server failed")
		}
	}
	stopStreams()
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			serveErr = errors.Join(serveErr, errors.New("relay shutdown timed out"))
			_ = server.Close()
		}
	}
	for completed < 2 {
		<-finished
		completed++
	}
	return serveErr
}
