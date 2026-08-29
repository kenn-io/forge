// Package fleetsetup installs the local half of a private HTTPS Forge.
package fleetsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/daemon"
)

const (
	defaultPort          = 8091
	defaultHTTPSTimeout  = 30 * time.Second
	managedFileMarker    = "Managed by kenn-forge fleet setup"
	defaultMacTailscale  = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
	publicationTailscale = "tailscale"
	publicationExternal  = "external"
)

// Role is the local topology requested by setup.
type Role string

const (
	RoleHub   Role = "hub"
	RoleSpoke Role = "spoke"
)

// Options contains explicit setup inputs. Empty discovery-backed values are
// resolved by Plan before any owned state is changed.
type Options struct {
	Role           Role
	ConfigPath     string
	DataDir        string
	BinaryPath     string
	User           string
	TailscaleLogin string
	TailscaleDNS   string
	Origin         string
	Port           int
	Tailscale      bool
}

// Plan is the complete, displayable setup intent.
type Plan struct {
	Role           Role
	ConfigPath     string
	DataDir        string
	BinaryPath     string
	User           string
	UID            int
	HomeDir        string
	PathEnv        string
	TailscaleLogin string
	TailscaleDNS   string
	Origin         string
	AllowedHost    string
	Publication    string
	Host           string
	Port           int
	ServicePath    string
	ServiceKind    string
	ServiceLabel   string
	EnableLinger   bool

	serveAlreadyConfigured bool
	tailscaleCommand       string
}

// Result reports the stable identity and URL produced by Apply.
type Result struct {
	Origin string
	NodeID string
	Role   Role
}

type commandResult struct {
	stdout []byte
	stderr []byte
}

type dependencies struct {
	goos              string
	currentUser       func() (*user.User, error)
	executable        func() (string, error)
	lookupPath        func(string) (string, error)
	run               func(context.Context, string, ...string) (commandResult, error)
	readFile          func(string) ([]byte, error)
	writeFile         func(string, []byte, os.FileMode) error
	remove            func(string) error
	stat              func(string) (os.FileInfo, error)
	httpClient        *http.Client
	readinessTimeout  time.Duration
	acquireConfigLock func(context.Context, string) (setupLock, error)
	ensureNodeID      func(string) (string, error)
	checkPortOwner    func(context.Context, string, int, string) error
	tailscaleAppPath  string
}

type setupLock interface {
	Release() error
}

// Runner discovers, applies, and verifies one local fleet setup.
type Runner struct {
	deps dependencies
}

// NewRunner returns the real host-backed setup runner.
func NewRunner() *Runner {
	return &Runner{deps: defaultDependencies()}
}

func defaultDependencies() dependencies {
	return dependencies{
		goos:             runtime.GOOS,
		currentUser:      user.Current,
		executable:       os.Executable,
		lookupPath:       exec.LookPath,
		run:              runCommand,
		readFile:         os.ReadFile,
		writeFile:        atomicWriteFile,
		remove:           os.Remove,
		stat:             os.Stat,
		httpClient:       &http.Client{Timeout: defaultHTTPSTimeout},
		readinessTimeout: defaultHTTPSTimeout,
		acquireConfigLock: func(ctx context.Context, configPath string) (setupLock, error) {
			store, err := daemonruntime.Store()
			if err != nil {
				return nil, err
			}
			return daemonruntime.AcquireConfigLifecycleLock(ctx, store, configPath)
		},
		ensureNodeID: runtimelock.EnsureNodeID,
		checkPortOwner: func(ctx context.Context, host string, port int, dataDir string) error {
			store, err := daemonruntime.Store()
			if err != nil {
				return fmt.Errorf("inspect daemon runtime records: %w", err)
			}
			return checkLocalPortOwner(ctx, host, port, dataDir, store)
		},
		tailscaleAppPath: defaultMacTailscale,
	}
}

func runCommand(ctx context.Context, name string, args ...string) (commandResult, error) {
	command := procutil.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			return result, fmt.Errorf("run %s: %w", name, err)
		}
		return result, fmt.Errorf("run %s: %w: %s", name, err, message)
	}
	return result, nil
}

// Plan resolves and validates every input without changing Forge, service, or
// Tailscale state.
func (r *Runner) Plan(ctx context.Context, options Options) (Plan, error) {
	if options.Tailscale == (strings.TrimSpace(options.Origin) != "") {
		return Plan{}, errors.New("choose exactly one publication mode: --tailscale or --origin")
	}
	if options.Role != RoleHub && options.Role != RoleSpoke {
		return Plan{}, fmt.Errorf("unsupported fleet setup role %q", options.Role)
	}
	if r.deps.goos != "linux" && r.deps.goos != "darwin" {
		return Plan{}, fmt.Errorf("fleet setup supports Linux and macOS, not %s", r.deps.goos)
	}
	current, err := r.deps.currentUser()
	if err != nil {
		return Plan{}, fmt.Errorf("resolve current user: %w", err)
	}
	selectedUser := strings.TrimSpace(options.User)
	if selectedUser == "" {
		selectedUser = current.Username
	}
	if selectedUser != current.Username {
		return Plan{}, fmt.Errorf("selected user %q must match the current user %q", selectedUser, current.Username)
	}
	uid, err := strconv.Atoi(current.Uid)
	if err != nil || uid < 0 {
		return Plan{}, fmt.Errorf("resolve numeric user ID %q", current.Uid)
	}

	configPath := strings.TrimSpace(options.ConfigPath)
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	configPath, err = daemonruntime.CanonicalConfigPath(configPath)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve config path: %w", err)
	}
	existing, err := loadExistingConfig(configPath, r.deps.stat)
	if err != nil {
		return Plan{}, err
	}

	dataDir := strings.TrimSpace(options.DataDir)
	if dataDir == "" && existing != nil {
		dataDir = existing.DataDir
	}
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	dataDir, err = config.CanonicalDataDir(dataDir)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve data directory: %w", err)
	}
	if existing != nil && existing.DataDir != dataDir {
		return Plan{}, fmt.Errorf("existing config uses data_dir %q; setup will not move it to %q", existing.DataDir, dataDir)
	}

	port := options.Port
	if port == 0 && existing != nil {
		port = existing.Port
	}
	if port == 0 {
		port = defaultPort
	}
	if port < 1 || port > 65535 {
		return Plan{}, fmt.Errorf("port must be between 1 and 65535, got %d", port)
	}
	if err := r.deps.checkPortOwner(ctx, "127.0.0.1", port, dataDir); err != nil {
		return Plan{}, err
	}

	binaryPath := strings.TrimSpace(options.BinaryPath)
	if binaryPath == "" {
		binaryPath, err = r.deps.executable()
		if err != nil {
			return Plan{}, fmt.Errorf("resolve kenn-forge binary: %w", err)
		}
	}
	binaryPath, err = filepath.Abs(binaryPath)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve kenn-forge binary path: %w", err)
	}
	if info, statErr := r.deps.stat(binaryPath); statErr != nil {
		return Plan{}, fmt.Errorf("inspect kenn-forge binary: %w", statErr)
	} else if info.IsDir() || info.Mode()&0o111 == 0 {
		return Plan{}, fmt.Errorf("kenn-forge binary %q is not executable", binaryPath)
	}
	publication := publicationExternal
	origin := strings.TrimSpace(options.Origin)
	tailnet := tailscaleDiscovery{}
	tailscaleCommand := ""
	if options.Tailscale {
		publication = publicationTailscale
		tailscaleCommand, err = r.resolveTailscaleCommand()
		if err != nil {
			return Plan{}, err
		}
		tailnet, err = r.discoverTailscale(
			ctx, tailscaleCommand, options.TailscaleDNS, options.TailscaleLogin,
		)
		if err != nil {
			return Plan{}, err
		}
		origin = "https://" + tailnet.DNSName
	}
	origin, err = federation.CanonicalOrigin(origin)
	if err != nil {
		return Plan{}, fmt.Errorf("canonicalize public origin: %w", err)
	}
	parsedOrigin, err := url.Parse(origin)
	if err != nil {
		return Plan{}, fmt.Errorf("parse public origin: %w", err)
	}

	service := servicePlan(r.deps.goos, current.HomeDir, uid)
	for _, command := range service.requiredCommands() {
		if _, err := r.deps.lookupPath(command); err != nil {
			return Plan{}, fmt.Errorf("%s is required for %s", command, service.Kind)
		}
	}
	if err := r.checkServiceOwnership(service.Path); err != nil {
		return Plan{}, err
	}
	enableLinger := false
	if r.deps.goos == "linux" {
		linger, err := r.deps.run(
			ctx, "loginctl", "show-user", selectedUser, "-p", "Linger", "--value",
		)
		if err != nil {
			return Plan{}, fmt.Errorf("inspect systemd user lingering: %w", err)
		}
		switch strings.TrimSpace(string(linger.stdout)) {
		case "yes":
		case "no":
			enableLinger = true
		default:
			return Plan{}, fmt.Errorf("unexpected systemd linger status %q", strings.TrimSpace(string(linger.stdout)))
		}
	}
	serveState := serveAbsent
	if publication == publicationTailscale {
		serveState, err = r.inspectServe(ctx, tailscaleCommand, tailnet.DNSName, port)
		if err != nil {
			return Plan{}, err
		}
	}
	plan := Plan{
		Role: options.Role, ConfigPath: configPath, DataDir: dataDir,
		BinaryPath: binaryPath, User: selectedUser, UID: uid,
		HomeDir: current.HomeDir, PathEnv: os.Getenv("PATH"),
		TailscaleLogin: tailnet.Login, TailscaleDNS: tailnet.DNSName,
		Origin: origin, AllowedHost: parsedOrigin.Host,
		Publication: publication, Host: "127.0.0.1", Port: port,
		ServicePath: service.Path, ServiceKind: service.Kind,
		ServiceLabel: service.Label, EnableLinger: enableLinger,
		serveAlreadyConfigured: serveState == serveExact,
		tailscaleCommand:       tailscaleCommand,
	}
	if err := validateCandidate(plan, existing); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func loadExistingConfig(path string, stat func(string) (os.FileInfo, error)) (*config.Config, error) {
	if _, err := stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect config: %w", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func validateCandidate(plan Plan, existing *config.Config) error {
	candidate := &config.Config{DataDir: plan.DataDir}
	if existing != nil {
		copy := *existing
		candidate = &copy
	}
	if err := configureCandidate(candidate, plan); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "kenn-forge-setup-plan-*")
	if err != nil {
		return fmt.Errorf("create setup validation directory: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := candidate.Save(filepath.Join(dir, "config.toml")); err != nil {
		return fmt.Errorf("validate setup config: %w", err)
	}
	return nil
}

func configureCandidate(candidate *config.Config, plan Plan) error {
	role := candidate.Fleet.RoleOrDefault()
	currentOrigin := candidate.Fleet.BaseURL
	if currentOrigin != "" && currentOrigin != plan.Origin {
		switch {
		case role == config.FleetRoleSpoke:
			return errors.New("an enrolled spoke must be restored to standalone state before changing its federation origin")
		case role == config.FleetRoleHub && len(candidate.Fleet.Members) > 0:
			return errors.New("a hub must revoke its members before changing its federation origin")
		}
	}
	switch plan.Role {
	case RoleHub:
		if role == config.FleetRoleSpoke || candidate.Fleet.Hub != nil {
			return errors.New("an enrolled spoke must be revoked and restored to standalone state before hub setup")
		}
		candidate.Fleet.Role = config.FleetRoleHub
	case RoleSpoke:
		if role == config.FleetRoleHub && len(candidate.Fleet.Members) > 0 {
			return errors.New("a hub with active members cannot be converted into a spoke")
		}
		// Enrollment owns the spoke role and hub binding. A repeated
		// setup of an already-active spoke preserves both.
		if role != config.FleetRoleSpoke {
			candidate.Fleet.Role = ""
		}
	default:
		return fmt.Errorf("unsupported setup role %q", plan.Role)
	}
	candidate.Host = plan.Host
	candidate.Port = plan.Port
	candidate.BasePath = "/"
	candidate.DataDir = plan.DataDir
	candidate.AllowedHosts = []string{plan.AllowedHost}
	candidate.TrustReverseProxy = false
	candidate.API.RequireAuth = true
	candidate.API.TailscaleServe = config.TailscaleServeAPI{}
	if plan.Publication == publicationTailscale {
		candidate.API.TailscaleServe = config.TailscaleServeAPI{
			Enabled: true, AllowedUsers: []string{plan.TailscaleLogin},
		}
	}
	candidate.Fleet.Enabled = true
	candidate.Fleet.BaseURL = plan.Origin
	return nil
}

func (r *Runner) checkServiceOwnership(path string) error {
	data, err := r.deps.readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect service file: %w", err)
	}
	if !bytes.Contains(data, []byte(managedFileMarker)) {
		return fmt.Errorf("service file %q is not owned by fleet setup; move or remove it explicitly", path)
	}
	return nil
}

func checkLocalPortOwner(
	ctx context.Context,
	host string,
	port int,
	dataDir string,
	store daemon.RuntimeStore,
) error {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err == nil {
		return listener.Close()
	}
	expectedAddress := net.JoinHostPort(host, strconv.Itoa(port))
	records, err := store.List()
	if err != nil {
		return fmt.Errorf("inspect daemon runtime records: %w", err)
	}
	for _, record := range records {
		if record.Address != expectedAddress {
			continue
		}
		_, verified, err := daemonruntime.FindVerifiedRecord(ctx, record, dataDir)
		if err != nil {
			return fmt.Errorf("verify loopback port %d owner: %w", port, err)
		}
		if verified {
			return nil
		}
	}
	return fmt.Errorf("loopback port %d is owned by another process", port)
}

func (r *Runner) applyLinger(ctx context.Context, plan Plan, transaction *transaction) error {
	if _, err := r.deps.run(ctx, "loginctl", "enable-linger", plan.User); err != nil {
		return fmt.Errorf("enable systemd user lingering: %w", err)
	}
	transaction.record(func(ctx context.Context) error {
		_, err := r.deps.run(ctx, "loginctl", "disable-linger", plan.User)
		return err
	})
	return nil
}

// Apply installs the service definition, writes the planned config, configures
// Serve, and verifies both the loopback and canonical HTTPS boundaries.
// Completed setup mutations are undone in reverse order on failure, restoring
// the previous config before the previous service is restarted.
func (r *Runner) Apply(ctx context.Context, plan Plan) (result Result, resultErr error) {
	configLock, err := r.deps.acquireConfigLock(ctx, plan.ConfigPath)
	if err != nil {
		return Result{}, fmt.Errorf("acquire config lifecycle lock: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, configLock.Release())
	}()

	transaction := transaction{}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, transaction.rollback(ctx))
		}
	}()

	if err := r.applyService(ctx, plan, &transaction); err != nil {
		return Result{}, err
	}
	if plan.EnableLinger {
		if err := r.applyLinger(ctx, plan, &transaction); err != nil {
			return Result{}, err
		}
	}
	if err := r.applyConfig(plan, &transaction); err != nil {
		return Result{}, err
	}
	if plan.Publication == publicationTailscale && !plan.serveAlreadyConfigured {
		if err := r.applyServe(ctx, plan, &transaction); err != nil {
			return Result{}, err
		}
	}
	if err := r.restartService(ctx, plan); err != nil {
		return Result{}, err
	}
	nodeID, err := r.deps.ensureNodeID(plan.DataDir)
	if err != nil {
		return Result{}, fmt.Errorf("read daemon identity: %w", err)
	}
	if err := r.verifyReadiness(ctx, plan, nodeID); err != nil {
		return Result{}, err
	}
	transaction.commit()
	return Result{Origin: plan.Origin, NodeID: nodeID, Role: plan.Role}, nil
}

type undoFunc func(context.Context) error

type transaction struct {
	undos     []undoFunc
	committed bool
}

func (t *transaction) record(undo undoFunc) {
	t.undos = append(t.undos, undo)
}

func (t *transaction) commit() {
	t.committed = true
}

func (t *transaction) rollback(ctx context.Context) error {
	if t.committed {
		return nil
	}
	var rollbackErrors []error
	for _, undo := range slices.Backward(t.undos) {
		if err := undo(ctx); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	return errors.Join(rollbackErrors...)
}

func (r *Runner) applyConfig(plan Plan, transaction *transaction) error {
	previous, err := r.deps.readFile(plan.ConfigPath)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("snapshot config: %w", err)
	}
	candidate := &config.Config{DataDir: plan.DataDir}
	if existed {
		candidate, err = config.Load(plan.ConfigPath)
		if err != nil {
			return fmt.Errorf("reload config under lifecycle lock: %w", err)
		}
	}
	if err := configureCandidate(candidate, plan); err != nil {
		return err
	}
	if err := candidate.Save(plan.ConfigPath); err != nil {
		return fmt.Errorf("save setup config: %w", err)
	}
	transaction.record(func(context.Context) error {
		if existed {
			return r.deps.writeFile(plan.ConfigPath, previous, 0o600)
		}
		if err := r.deps.remove(plan.ConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
	return nil
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".kenn-forge-setup-*")
	if err != nil {
		return err
	}
	tmpPath := file.Name()
	defer os.Remove(tmpPath)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (r *Runner) verifyReadiness(ctx context.Context, plan Plan, nodeID string) error {
	loopback := fmt.Sprintf("http://127.0.0.1:%d/healthz", plan.Port)
	if err := waitHTTP(ctx, r.deps.httpClient, loopback, "", r.deps.readinessTimeout, nil); err != nil {
		return fmt.Errorf("loopback readiness: %w", err)
	}
	var snapshot struct {
		Hosts []struct {
			NodeID         string `json:"nodeID"`
			Kind           string `json:"kind"`
			FederationRole string `json:"federationRole"`
		} `json:"hosts"`
	}
	bearer := ""
	if plan.Publication == publicationExternal {
		rawToken, err := r.deps.readFile(runtimelock.AuthTokenPath(plan.DataDir))
		if err != nil {
			return fmt.Errorf("read daemon bearer for HTTPS verification: %w", err)
		}
		bearer = strings.TrimSpace(string(rawToken))
		if bearer == "" {
			return errors.New("daemon bearer for HTTPS verification is empty")
		}
	}
	if err := waitHTTP(
		ctx, r.deps.httpClient, plan.Origin+"/api/v1/snapshot?include_peers=true", bearer,
		r.deps.readinessTimeout, &snapshot,
	); err != nil {
		return fmt.Errorf("canonical HTTPS readiness: %w", err)
	}
	for _, host := range snapshot.Hosts {
		if host.Kind != "self" {
			continue
		}
		if host.NodeID != nodeID {
			return fmt.Errorf("canonical HTTPS returned daemon %q, expected %q", host.NodeID, nodeID)
		}
		if plan.Role == RoleHub && host.FederationRole != string(RoleHub) {
			return fmt.Errorf("canonical HTTPS returned role %q, expected hub", host.FederationRole)
		}
		return nil
	}
	return errors.New("canonical HTTPS snapshot did not contain the local Forge")
}

func waitHTTP(
	ctx context.Context,
	client *http.Client,
	url string,
	bearer string,
	timeout time.Duration,
	result any,
) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, err := client.Do(request)
		if err == nil {
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if result == nil {
					_ = response.Body.Close()
					return nil
				}
				decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(result)
				_ = response.Body.Close()
				if decodeErr == nil {
					return nil
				}
				lastErr = decodeErr
			} else {
				lastErr = fmt.Errorf("HTTP %s", response.Status)
				_ = response.Body.Close()
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastErr)
		case <-deadline.C:
			return errors.Join(errors.New("readiness deadline exceeded"), lastErr)
		case <-ticker.C:
		}
	}
}
