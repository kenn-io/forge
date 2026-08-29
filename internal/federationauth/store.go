package federationauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

const credentialStoreVersion = 1

type inboundCredential struct {
	NodeID      string  `json:"node_id"`
	TokenDigest string  `json:"token_digest"`
	Scopes      []Scope `json:"scopes"`
}

type pendingInboundCredential struct {
	ReservationID string  `json:"reservation_id"`
	TokenDigest   string  `json:"token_digest"`
	Scopes        []Scope `json:"scopes"`
}

// Credential is a readable outbound bearer and its destination identity.
type Credential struct {
	NodeID string  `json:"node_id"`
	Token  string  `json:"token"`
	Scopes []Scope `json:"scopes"`
}

type persistedStore struct {
	Version  int                        `json:"version"`
	Pending  []pendingInboundCredential `json:"pending_inbound"`
	Inbound  []inboundCredential        `json:"inbound"`
	Outbound []Credential               `json:"outbound"`
}

type credentialSnapshot struct {
	inbound  []inboundCredential
	outbound map[string]Credential
}

// Store persists inbound token digests and readable outbound credentials.
// Mutations publish a new immutable snapshot only after the atomic file write
// succeeds, so revocation is visible to the very next in-process request.
type Store struct {
	path     string
	mu       sync.Mutex
	state    persistedStore
	snapshot atomic.Pointer[credentialSnapshot]
}

// Open loads or creates a credential store at path.
func Open(path string) (*Store, error) {
	path = filepath.Clean(path)
	if path == "." || strings.TrimSpace(path) == "" {
		return nil, errors.New("federation credential store path is required")
	}
	state, exists, err := readPersistedStore(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		state = persistedStore{
			Version: credentialStoreVersion,
			Pending: []pendingInboundCredential{},
			Inbound: []inboundCredential{}, Outbound: []Credential{},
		}
		if err := writePersistedStore(path, state); err != nil {
			return nil, err
		}
	} else if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("restrict federation credential store: %w", err)
	}
	store := &Store{path: path, state: state}
	store.snapshot.Store(snapshotFor(state))
	return store, nil
}

// DefaultStorePath returns the credential path under dataDir.
func DefaultStorePath(dataDir string) string {
	return filepath.Join(dataDir, "federation-credentials.json")
}

// Path returns the credential store's persistence path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// MintInbound creates and persists an inbound credential while returning its
// bearer exactly once to the caller.
func (s *Store) MintInbound(nodeID string, scopes []Scope) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := s.StoreInbound(nodeID, token, scopes); err != nil {
		return "", err
	}
	return token, nil
}

// ReserveInbound persists a digest-only credential before its authenticated
// peer subject is known. Pending credentials cannot authenticate until bound.
func (s *Store) ReserveInbound(reservationID string, scopes []Scope) (string, error) {
	if !validNodeID(reservationID) {
		return "", errors.New("federation credential reservation ID must be 32 lowercase hexadecimal characters")
	}
	normalized, err := normalizeScopes(scopes)
	if err != nil {
		return "", err
	}
	if len(normalized) == 0 {
		return "", errors.New("federation credential requires at least one scope")
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	record := pendingInboundCredential{
		ReservationID: reservationID,
		TokenDigest:   digestToken(token),
		Scopes:        normalized,
	}
	if err := s.mutate(func(state *persistedStore) {
		state.Pending = slices.DeleteFunc(
			state.Pending,
			func(existing pendingInboundCredential) bool {
				return existing.ReservationID == reservationID
			},
		)
		state.Pending = append(state.Pending, record)
	}); err != nil {
		return "", err
	}
	return token, nil
}

// BindInbound activates a pending digest under an exact peer spoke subject.
func (s *Store) BindInbound(reservationID, nodeID string) error {
	if !validNodeID(reservationID) {
		return errors.New("federation credential reservation ID must be 32 lowercase hexadecimal characters")
	}
	if !validNodeID(nodeID) {
		return errors.New("federation credential node ID must be 32 lowercase hexadecimal characters")
	}
	found := false
	err := s.mutate(func(state *persistedStore) {
		for _, pending := range state.Pending {
			if pending.ReservationID != reservationID {
				continue
			}
			found = true
			state.Inbound = replaceInboundForNode(state.Inbound, inboundCredential{
				NodeID: nodeID, TokenDigest: pending.TokenDigest,
				Scopes: slices.Clone(pending.Scopes),
			})
			break
		}
		if found {
			state.Pending = slices.DeleteFunc(
				state.Pending,
				func(pending pendingInboundCredential) bool {
					return pending.ReservationID == reservationID
				},
			)
		}
	})
	if err != nil {
		return err
	}
	if !found {
		return errors.New("pending federation credential not found")
	}
	return nil
}

// RevokePending removes a not-yet-bound inbound credential.
func (s *Store) RevokePending(reservationID string) error {
	return s.mutate(func(state *persistedStore) {
		state.Pending = slices.DeleteFunc(
			state.Pending,
			func(pending pendingInboundCredential) bool {
				return pending.ReservationID == reservationID
			},
		)
	})
}

// StoreInbound persists only the digest of a supplied inbound bearer. A new
// credential for the same spoke replaces the old one synchronously.
func (s *Store) StoreInbound(nodeID, token string, scopes []Scope) error {
	normalized, err := validateCredential(nodeID, token, scopes)
	if err != nil {
		return err
	}
	record := inboundCredential{
		NodeID: nodeID, TokenDigest: digestToken(token), Scopes: normalized,
	}
	return s.mutate(func(state *persistedStore) {
		state.Inbound = replaceInboundForNode(state.Inbound, record)
	})
}

// MintOutbound creates and persists a bearer this daemon will send to nodeID.
func (s *Store) MintOutbound(nodeID string, scopes []Scope) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := s.StoreOutbound(nodeID, token, scopes); err != nil {
		return "", err
	}
	return token, nil
}

// StoreOutbound persists a readable bearer this daemon must attach to requests.
func (s *Store) StoreOutbound(nodeID, token string, scopes []Scope) error {
	normalized, err := validateCredential(nodeID, token, scopes)
	if err != nil {
		return err
	}
	record := Credential{NodeID: nodeID, Token: token, Scopes: normalized}
	return s.mutate(func(state *persistedStore) {
		state.Outbound = replaceOutboundForNode(state.Outbound, record)
	})
}

// UpdateInboundScopes changes an existing inbound credential's grant without
// rotating its bearer.
func (s *Store) UpdateInboundScopes(nodeID string, scopes []Scope) error {
	normalized, err := validateCredentialScopes(nodeID, scopes)
	if err != nil {
		return err
	}
	found := false
	err = s.mutate(func(state *persistedStore) {
		for index := range state.Inbound {
			if state.Inbound[index].NodeID == nodeID {
				state.Inbound[index].Scopes = slices.Clone(normalized)
				found = true
				break
			}
		}
	})
	if err != nil {
		return err
	}
	if !found {
		return errors.New("inbound federation credential not found")
	}
	return nil
}

// UpdateOutboundScopes changes an existing outbound credential's grant
// without rotating its bearer.
func (s *Store) UpdateOutboundScopes(nodeID string, scopes []Scope) error {
	normalized, err := validateCredentialScopes(nodeID, scopes)
	if err != nil {
		return err
	}
	found := false
	err = s.mutate(func(state *persistedStore) {
		for index := range state.Outbound {
			if state.Outbound[index].NodeID == nodeID {
				state.Outbound[index].Scopes = slices.Clone(normalized)
				found = true
				break
			}
		}
	})
	if err != nil {
		return err
	}
	if !found {
		return errors.New("outbound federation credential not found")
	}
	return nil
}

// Authenticate compares the supplied token against every inbound digest in
// constant time and returns a detached principal on success.
func (s *Store) Authenticate(token string) (Principal, bool) {
	if s == nil || token == "" {
		return Principal{}, false
	}
	digest := digestToken(token)
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		return Principal{}, false
	}
	match := -1
	for index, credential := range snapshot.inbound {
		if subtle.ConstantTimeCompare(
			[]byte(digest), []byte(credential.TokenDigest),
		) == 1 {
			match = index
		}
	}
	if match < 0 {
		return Principal{}, false
	}
	return principalFor(snapshot.inbound[match]), true
}

// Outbound returns a detached readable credential for nodeID.
func (s *Store) Outbound(nodeID string) (Credential, bool) {
	if s == nil {
		return Credential{}, false
	}
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		return Credential{}, false
	}
	credential, ok := snapshot.outbound[nodeID]
	if !ok {
		return Credential{}, false
	}
	credential.Scopes = slices.Clone(credential.Scopes)
	return credential, true
}

// RevokeInbound removes the credential matching token before returning.
func (s *Store) RevokeInbound(token string) error {
	if token == "" {
		return nil
	}
	digest := digestToken(token)
	return s.mutate(func(state *persistedStore) {
		kept := state.Inbound[:0]
		for _, credential := range state.Inbound {
			if subtle.ConstantTimeCompare(
				[]byte(digest), []byte(credential.TokenDigest),
			) != 1 {
				kept = append(kept, credential)
			}
		}
		state.Inbound = kept
	})
}

// RevokeInboundNode removes the inbound credential for nodeID.
func (s *Store) RevokeInboundNode(nodeID string) error {
	return s.mutate(func(state *persistedStore) {
		state.Inbound = slices.DeleteFunc(state.Inbound, func(credential inboundCredential) bool {
			return credential.NodeID == nodeID
		})
	})
}

// RevokeOutbound removes the outbound credential for nodeID.
func (s *Store) RevokeOutbound(nodeID string) error {
	return s.mutate(func(state *persistedStore) {
		state.Outbound = slices.DeleteFunc(state.Outbound, func(credential Credential) bool {
			return credential.NodeID == nodeID
		})
	})
}

func (s *Store) mutate(apply func(*persistedStore)) error {
	if s == nil {
		return errors.New("federation credential store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clonePersistedStore(s.state)
	apply(&next)
	sortPersistedStore(&next)
	if err := writePersistedStore(s.path, next); err != nil {
		return err
	}
	s.state = next
	s.snapshot.Store(snapshotFor(next))
	return nil
}

func validateCredential(nodeID, token string, scopes []Scope) ([]Scope, error) {
	normalized, err := validateCredentialScopes(nodeID, scopes)
	if err != nil {
		return nil, err
	}
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("federation credential token must be non-empty and contain no whitespace")
	}
	return normalized, nil
}

func validateCredentialScopes(nodeID string, scopes []Scope) ([]Scope, error) {
	if !validNodeID(nodeID) {
		return nil, fmt.Errorf("federation credential node ID must be 32 lowercase hexadecimal characters")
	}
	normalized, err := normalizeScopes(scopes)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return nil, errors.New("federation credential requires at least one scope")
	}
	return normalized, nil
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate federation credential: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func digestToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func readPersistedStore(path string) (persistedStore, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return persistedStore{}, false, nil
	}
	if err != nil {
		return persistedStore{}, false, fmt.Errorf("open federation credential store: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var state persistedStore
	if err := decoder.Decode(&state); err != nil {
		return persistedStore{}, false, fmt.Errorf("decode federation credential store: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return persistedStore{}, false, err
	}
	if err := validatePersistedStore(&state); err != nil {
		return persistedStore{}, false, err
	}
	return state, true, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode federation credential store trailer: %w", err)
	}
	return errors.New("decode federation credential store: multiple JSON values")
}

func validatePersistedStore(state *persistedStore) error {
	if state.Version != credentialStoreVersion {
		return fmt.Errorf(
			"federation credential store version must be %d, got %d",
			credentialStoreVersion, state.Version,
		)
	}
	pendingIDs := make(map[string]struct{}, len(state.Pending))
	for index := range state.Pending {
		credential := &state.Pending[index]
		if !validNodeID(credential.ReservationID) {
			return fmt.Errorf("pending federation credential %d has invalid reservation ID", index)
		}
		if err := validateTokenDigest(credential.TokenDigest); err != nil {
			return fmt.Errorf("pending federation credential %d: %w", index, err)
		}
		var err error
		credential.Scopes, err = normalizeScopes(credential.Scopes)
		if err != nil || len(credential.Scopes) == 0 {
			if err == nil {
				err = errors.New("scope set is empty")
			}
			return fmt.Errorf("pending federation credential %d scopes: %w", index, err)
		}
		if _, duplicate := pendingIDs[credential.ReservationID]; duplicate {
			return fmt.Errorf("duplicate pending federation credential %s", credential.ReservationID)
		}
		pendingIDs[credential.ReservationID] = struct{}{}
	}
	inboundNodes := make(map[string]struct{}, len(state.Inbound))
	for index := range state.Inbound {
		credential := &state.Inbound[index]
		if !validNodeID(credential.NodeID) {
			return fmt.Errorf("inbound federation credential %d has invalid node ID", index)
		}
		if err := validateTokenDigest(credential.TokenDigest); err != nil {
			return fmt.Errorf("inbound federation credential %d: %w", index, err)
		}
		var err error
		credential.Scopes, err = normalizeScopes(credential.Scopes)
		if err != nil || len(credential.Scopes) == 0 {
			if err == nil {
				err = errors.New("scope set is empty")
			}
			return fmt.Errorf("inbound federation credential %d scopes: %w", index, err)
		}
		if _, duplicate := inboundNodes[credential.NodeID]; duplicate {
			return fmt.Errorf("duplicate inbound federation credential for spoke %s", credential.NodeID)
		}
		inboundNodes[credential.NodeID] = struct{}{}
	}
	outboundNodes := make(map[string]struct{}, len(state.Outbound))
	for index := range state.Outbound {
		credential := &state.Outbound[index]
		normalized, err := validateCredential(
			credential.NodeID, credential.Token, credential.Scopes,
		)
		if err != nil {
			return fmt.Errorf("outbound federation credential %d: %w", index, err)
		}
		credential.Scopes = normalized
		if _, duplicate := outboundNodes[credential.NodeID]; duplicate {
			return fmt.Errorf("duplicate outbound federation credential for spoke %s", credential.NodeID)
		}
		outboundNodes[credential.NodeID] = struct{}{}
	}
	sortPersistedStore(state)
	return nil
}

func writePersistedStore(path string, state persistedStore) error {
	parent := filepath.Dir(path)
	tmp, err := os.CreateTemp(parent, ".federation-credentials.*.tmp")
	if err != nil {
		return fmt.Errorf("create federation credential store temp file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict federation credential store temp file: %w", err)
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode federation credential store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync federation credential store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close federation credential store: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish federation credential store: %w", err)
	}
	committed = true
	return nil
}

func clonePersistedStore(state persistedStore) persistedStore {
	clone := persistedStore{
		Version:  state.Version,
		Pending:  make([]pendingInboundCredential, len(state.Pending)),
		Inbound:  make([]inboundCredential, len(state.Inbound)),
		Outbound: make([]Credential, len(state.Outbound)),
	}
	for index, credential := range state.Pending {
		credential.Scopes = slices.Clone(credential.Scopes)
		clone.Pending[index] = credential
	}
	for index, credential := range state.Inbound {
		credential.Scopes = slices.Clone(credential.Scopes)
		clone.Inbound[index] = credential
	}
	for index, credential := range state.Outbound {
		credential.Scopes = slices.Clone(credential.Scopes)
		clone.Outbound[index] = credential
	}
	return clone
}

func sortPersistedStore(state *persistedStore) {
	slices.SortFunc(state.Pending, func(left, right pendingInboundCredential) int {
		return strings.Compare(left.ReservationID, right.ReservationID)
	})
	slices.SortFunc(state.Inbound, func(left, right inboundCredential) int {
		return strings.Compare(left.NodeID, right.NodeID)
	})
	slices.SortFunc(state.Outbound, func(left, right Credential) int {
		return strings.Compare(left.NodeID, right.NodeID)
	})
}

func validateTokenDigest(digest string) error {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size ||
		digest != strings.ToLower(digest) {
		return errors.New("invalid token digest")
	}
	return nil
}

func snapshotFor(state persistedStore) *credentialSnapshot {
	snapshot := &credentialSnapshot{
		inbound:  make([]inboundCredential, len(state.Inbound)),
		outbound: make(map[string]Credential, len(state.Outbound)),
	}
	for index, credential := range state.Inbound {
		credential.Scopes = slices.Clone(credential.Scopes)
		snapshot.inbound[index] = credential
	}
	for _, credential := range state.Outbound {
		credential.Scopes = slices.Clone(credential.Scopes)
		snapshot.outbound[credential.NodeID] = credential
	}
	return snapshot
}

func principalFor(credential inboundCredential) Principal {
	scopes := make(map[Scope]struct{}, len(credential.Scopes))
	for _, scope := range credential.Scopes {
		scopes[scope] = struct{}{}
	}
	return Principal{NodeID: credential.NodeID, Scopes: scopes}
}

func replaceInboundForNode(
	credentials []inboundCredential, replacement inboundCredential,
) []inboundCredential {
	result := make([]inboundCredential, 0, len(credentials)+1)
	for _, credential := range credentials {
		if credential.NodeID != replacement.NodeID {
			result = append(result, credential)
		}
	}
	return append(result, replacement)
}

func replaceOutboundForNode(
	credentials []Credential, replacement Credential,
) []Credential {
	result := make([]Credential, 0, len(credentials)+1)
	for _, credential := range credentials {
		if credential.NodeID != replacement.NodeID {
			result = append(result, credential)
		}
	}
	return append(result, replacement)
}
