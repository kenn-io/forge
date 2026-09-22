package devbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	"go.kenn.io/forge/internal/apiclient"
	controlclient "go.kenn.io/forge/internal/apiclient/devbox"
)

type Connection struct {
	Assignment
	ID         string `json:"id"`
	RegistryID string `json:"registry_id"`
	Revision   string `json:"revision"`
}

type savedConnection struct {
	ID          string  `json:"id"`
	RegistryURL string  `json:"registry_url"`
	Profile     Profile `json:"profile"`
}

// Connections holds native-only credentials. It has no browser credential projection.
type Connections struct {
	attribution map[string]attributionCacheEntry
	mu          sync.RWMutex
	path        string
	items       []savedConnection
	client      *http.Client
}

func OpenConnections(dataDir string) (*Connections, error) {
	c := &Connections{path: filepath.Join(dataDir, "devbox-connections.json"), client: &http.Client{
		Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 30 * time.Second}, CheckRedirect: refuseRedirect,
	}}
	raw, err := os.ReadFile(c.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(raw, &c.items); err != nil {
			return nil, fmt.Errorf("read devbox connections: %w", err)
		}
		if err := os.Chmod(c.path, 0o600); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *Connections) Close() { c.client.CloseIdleConnections() }

func (c *Connections) List() []Connection {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Connection, 0, len(c.items))
	for _, item := range c.items {
		out = append(out, Connection{Assignment: item.Profile.Assignment, ID: item.ID, RegistryID: item.Profile.RegistryID, Revision: item.Profile.Revision})
	}
	return out
}

func (c *Connections) Discover(ctx context.Context, registry string) (Discovery, error) {
	var discovery Discovery
	request, err := controlclient.NewGetAPIV1DevboxesRequest(ctx, registry)
	if err != nil {
		return discovery, err
	}
	err = c.registryRequest(ctx, registry, request, &discovery)
	if err == nil && (discovery.Protocol != Protocol || discovery.RegistryID == "" || discovery.GitHubUserID <= 0) {
		err = errors.New("registry returned an unsupported protocol or invalid identity")
	}
	discovery.RegistryURL = registry
	return discovery, err
}

func (c *Connections) Connect(ctx context.Context, registry, hostID string) (Connection, error) {
	discovery, err := c.Discover(ctx, registry)
	if err != nil {
		return Connection{}, err
	}
	var profile Profile
	request, err := controlclient.NewPostAPIV1DevboxesByHostIDConnectRequest(ctx, registry, &controlclient.PostAPIV1DevboxesByHostIDConnectRequestOptions{PathParams: &controlclient.PostAPIV1DevboxesByHostIDConnectPath{HostID: hostID}})
	if err != nil {
		return Connection{}, err
	}
	if err := c.registryRequest(ctx, registry, request, &profile); err != nil {
		return Connection{}, err
	}
	if profile.RegistryID != discovery.RegistryID || profile.GitHubUserID != discovery.GitHubUserID || profile.HostID != hostID || len(profile.Token) < 32 {
		return Connection{}, errors.New("registry connection identity differs from discovery")
	}
	if err := validateAssignment(profile.Assignment); err != nil {
		return Connection{}, err
	}
	if err := c.checkWorker(ctx, profile); err != nil {
		return Connection{}, err
	}
	digest := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%d", profile.RegistryID, profile.HostID, profile.GitHubUserID))
	id := hex.EncodeToString(digest[:16])
	item := savedConnection{ID: id, RegistryURL: registry, Profile: profile}
	c.mu.Lock()
	defer c.mu.Unlock()
	items := slices.Clone(c.items)
	if index := slices.IndexFunc(items, func(item savedConnection) bool { return item.ID == id }); index >= 0 {
		if items[index].Profile.WorkerIdentity != profile.WorkerIdentity {
			return Connection{}, errors.New("worker identity changed; remove this connection and explicitly add it again")
		}
		items[index] = item
	} else {
		items = append(items, item)
	}
	if err := c.save(items); err != nil {
		return Connection{}, err
	}
	return Connection{Assignment: profile.Assignment, ID: id, RegistryID: profile.RegistryID, Revision: profile.Revision}, nil
}

func (c *Connections) Remove(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	items := slices.DeleteFunc(slices.Clone(c.items), func(item savedConnection) bool { return item.ID == id })
	return c.save(items)
}

func (c *Connections) Refresh(ctx context.Context, id string) error {
	item, err := c.lookup(id)
	if err != nil {
		return err
	}
	_, err = c.Connect(ctx, item.RegistryURL, item.Profile.HostID)
	return err
}

func (c *Connections) lookup(id string) (savedConnection, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, item := range c.items {
		if item.ID == id {
			return item, nil
		}
	}
	return savedConnection{}, errors.New("devbox connection not found")
}

func (c *Connections) Check(ctx context.Context, id string) error {
	item, err := c.lookup(id)
	if err != nil {
		return err
	}
	return c.checkWorker(ctx, item.Profile)
}

func (c *Connections) checkWorker(ctx context.Context, profile Profile) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, err := c.profileClient(profile)
	if err != nil {
		return err
	}
	response, err := client.HTTP.GetExecutionWorkerRaw(ctx, client.Transport)
	if err != nil {
		return fmt.Errorf("devbox is offline: %w", err)
	}
	defer response.Body.Close()
	var identity WorkerIdentity
	if err := decodeResponse(response, &identity); err != nil {
		return err
	}
	if identity != profile.WorkerIdentity {
		return errors.New("devbox identity mismatch; check the account assignment before reconnecting")
	}
	return nil
}

// WorkerClient uses the same account bearer and transport as streaming proxies.
func (c *Connections) WorkerClient(id string) (*apiclient.Client, error) {
	item, err := c.lookup(id)
	if err != nil {
		return nil, err
	}
	return c.profileClient(item.Profile)
}

func (c *Connections) profileClient(profile Profile) (*apiclient.Client, error) {
	return apiclient.NewWithHTTPClient(profile.URL, c.client, runtime.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer "+profile.Token)
		return nil
	}))
}

func (c *Connections) Do(ctx context.Context, id, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	item, err := c.lookup(id)
	if err != nil {
		return nil, err
	}
	return c.profileRequest(ctx, item.Profile, method, path, body, headers)
}

// WorkerEndpoint is for the native WebSocket transport, never a browser response.
func (c *Connections) WorkerEndpoint(id string) (string, string, error) {
	item, err := c.lookup(id)
	if err != nil {
		return "", "", err
	}
	return strings.TrimRight(item.Profile.URL, "/"), item.Profile.Token, nil
}

func (c *Connections) profileRequest(ctx context.Context, profile Profile, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(profile.URL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"Content-Type", "Accept"} {
		if value := headers.Get(name); value != "" {
			request.Header.Set(name, value)
		}
	}
	request.Header.Set("Authorization", "Bearer "+profile.Token)
	return c.client.Do(request)
}

func (c *Connections) registryRequest(ctx context.Context, registry string, request *http.Request, out any) error {
	u, err := url.Parse(registry)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("configure an HTTPS devbox registry URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request = request.WithContext(ctx)
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("devbox registry unavailable: %w", err)
	}
	defer response.Body.Close()
	return decodeResponse(response, out)
}

func decodeResponse(response *http.Response, out any) error {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.UnmarshalRead(io.LimitReader(response.Body, 64<<10), &problem)
		if problem.Detail == "" {
			problem.Detail = http.StatusText(response.StatusCode)
		}
		return fmt.Errorf("devbox: %s (%d)", problem.Detail, response.StatusCode)
	}
	return json.UnmarshalRead(io.LimitReader(response.Body, 32<<20), out)
}

func (c *Connections) save(items []savedConnection) error {
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(c.path), ".devbox-connections-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, writeErr := io.Copy(temp, bytes.NewReader(data))
	if writeErr == nil {
		writeErr = temp.Sync()
	}
	closeErr := temp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), c.path); err != nil {
		return err
	}
	c.items = items
	return nil
}

func (c *Connections) RegistryURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.items) == 0 {
		return ""
	}
	return c.items[0].RegistryURL
}
