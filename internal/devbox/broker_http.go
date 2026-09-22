package devbox

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	controlclient "go.kenn.io/forge/internal/apiclient/devbox"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/tokenauth"
)

type peerUIDKey struct{}

func unixPeerContext(ctx context.Context, conn net.Conn) context.Context {
	uid, err := peerUID(conn)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, peerUIDKey{}, uid)
}

func brokerHandler(broker *Broker) http.Handler {
	mux := http.NewServeMux()
	cfg := huma.DefaultConfig("Execution worker GitHub credentials", "1")
	cfg.OpenAPIPath, cfg.DocsPath, cfg.SchemasPath = "", "", ""
	api := humago.New(mux, cfg)
	registerBrokerRoutes(api, broker)
	return mux
}

func registerBrokerRoutes(api huma.API, broker *Broker) {
	huma.Register(api, huma.Operation{
		OperationID: "devbox-credential", Method: http.MethodPost, Path: "/v1/credentials", MaxBodyBytes: 4096,
	}, func(ctx context.Context, input *struct{ Body CredentialRequest }) (*struct{ Body Credential }, error) {
		uid, ok := ctx.Value(peerUIDKey{}).(uint32)
		if !ok {
			return nil, httpapi.NewProblem(http.StatusUnauthorized, httpapi.CodeUnauthorized, "Unix peer identity is unavailable", nil)
		}
		credential, err := broker.Credential(ctx, uid, input.Body)
		if err != nil {
			return nil, httpapi.Forbidden(err.Error(), nil)
		}
		return &struct{ Body Credential }{Body: *credential}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "erase-devbox-credential", Method: http.MethodDelete, Path: "/v1/credentials",
	}, func(ctx context.Context, input *struct {
		Repository string `query:"repository" required:"true"`
	}) (*struct{}, error) {
		uid, ok := ctx.Value(peerUIDKey{}).(uint32)
		if !ok {
			return nil, httpapi.NewProblem(http.StatusUnauthorized, httpapi.CodeUnauthorized, "Unix peer identity is unavailable", nil)
		}
		if err := broker.Erase(uid, input.Repository); err != nil {
			return nil, httpapi.Forbidden(err.Error(), nil)
		}
		return &struct{}{}, nil
	})
}

func ServeBroker(ctx context.Context, cfg BrokerConfig) error {
	broker, err := NewBroker(cfg)
	if err != nil {
		return err
	}
	// SO_PEERCRED, checked on every request, is the account admission boundary.
	// Allow connecting without supplementary groups so an existing user manager
	// does not need a destructive restart when the broker is provisioned.
	return serveUnix(ctx, cfg.Socket, 0o666, &http.Server{
		Handler: brokerHandler(broker), ConnContext: unixPeerContext,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: time.Minute,
	})
}

// serveLocal owns both the listener and its shutdown goroutine.
func serveLocal(ctx context.Context, listener net.Listener, server *http.Server) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
	}()
	err := server.Serve(listener)
	cancel()
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type BrokerClient struct {
	http *http.Client
}

func NewBrokerClient(socket string) *BrokerClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		},
	}
	return &BrokerClient{http: &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: refuseRedirect}}
}

func (client *BrokerClient) Close() { client.http.CloseIdleConnections() }

func (client *BrokerClient) Credential(ctx context.Context, repository, profile string) (*Credential, error) {
	request, err := controlclient.NewDevboxCredentialRequest(ctx, "http://devbox.invalid", &controlclient.DevboxCredentialRequestOptions{Body: &controlclient.CredentialRequest{Repository: repository, Profile: controlclient.CredentialRequestProfile(profile)}})
	if err != nil {
		return nil, err
	}
	var credential Credential
	if err := client.request(request, &credential); err != nil {
		return nil, err
	}
	if credential.Token == "" || !credential.ExpiresAt.After(time.Now()) {
		return nil, errors.New("broker returned an empty or expired GitHub credential")
	}
	return &credential, nil
}

func (client *BrokerClient) Erase(ctx context.Context, repository string) error {
	request, err := controlclient.NewEraseDevboxCredentialRequest(ctx, "http://devbox.invalid", &controlclient.EraseDevboxCredentialRequestOptions{Query: &controlclient.EraseDevboxCredentialQuery{Repository: repository}})
	if err != nil {
		return err
	}
	return client.request(request, nil)
}

func (client *BrokerClient) request(request *http.Request, target any) error {
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("devbox GitHub broker unavailable: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(data, &problem); err != nil || problem.Detail == "" {
			return fmt.Errorf("devbox GitHub broker returned HTTP %d", response.StatusCode)
		}
		return fmt.Errorf("devbox GitHub: %s", problem.Detail)
	}
	if target == nil {
		return nil
	}
	return json.Unmarshal(data, target)
}

func (client *BrokerClient) SourceForRepo(provider, host, owner, name string) tokenauth.Source {
	return brokerGitSource{client: client, provider: provider, host: host, repository: owner + "/" + name}
}

func (client *BrokerClient) FallbackSource(host string) tokenauth.Source {
	return brokerGitSource{client: client, provider: "github", host: host}
}

type brokerGitSource struct {
	client     *BrokerClient
	provider   string
	host       string
	repository string
}

func (source brokerGitSource) Token(ctx context.Context) (string, error) {
	if source.provider != "github" || source.host != "github.com" {
		return "", errors.New("devbox GitHub broker only admits github.com repositories")
	}
	credential, err := source.client.Credential(ctx, source.repository, "git")
	if err != nil {
		return "", err
	}
	return credential.Token, nil
}

func (source brokerGitSource) Invalidate(_ string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = source.client.Erase(ctx, source.repository)
}

func (source brokerGitSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{Key: tokenauth.Key{Platform: source.provider, Host: source.host, Scope: "repo:" + source.repository}}
}
