package federation

import (
	"context"
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
	"time"
)

const enrollmentStoreVersion = 1

type oneTimeTokenRecord struct {
	Digest               string    `json:"digest"`
	ExpiresAt            time.Time `json:"expires_at"`
	HubID                string    `json:"hub_node_id"`
	HubName              string    `json:"hub_name,omitempty"`
	HubURL               string    `json:"hub_base_url"`
	ConsumedEnrollmentID string    `json:"consumed_enrollment_id,omitempty"`
	ConsumedNodeID       string    `json:"consumed_node_id,omitempty"`
}

type persistedEnrollmentStore struct {
	Version     int                  `json:"version"`
	Tokens      []oneTimeTokenRecord `json:"tokens"`
	Enrollments []Enrollment         `json:"enrollments"`
	Local       *LocalEnrollment     `json:"local,omitempty"`
}

type StoreOptions struct {
	Now func() time.Time
}

// Store persists hub enrollment records and the daemon's optional
// local hub binding. It never persists bearer plaintext.
type Store struct {
	path  string
	now   func() time.Time
	mu    sync.RWMutex
	state persistedEnrollmentStore
}

// DefaultStorePath returns the enrollment state path under dataDir.
func DefaultStorePath(dataDir string) string {
	return filepath.Join(dataDir, "federation-enrollments.json")
}

func Open(path string, options StoreOptions) (*Store, error) {
	path = filepath.Clean(path)
	if path == "." || strings.TrimSpace(path) == "" {
		return nil, errors.New("federation enrollment store path is required")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	state, exists, err := readEnrollmentStore(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		state = persistedEnrollmentStore{
			Version: enrollmentStoreVersion,
			Tokens:  []oneTimeTokenRecord{}, Enrollments: []Enrollment{},
		}
		if err := writeEnrollmentStore(path, state); err != nil {
			return nil, err
		}
	} else if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("restrict federation enrollment store: %w", err)
	}
	return &Store{path: path, now: now, state: state}, nil
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) CreateOneTimeToken(
	identity Identity, expiresAt time.Time,
) (EnrollmentToken, error) {
	identity, err := normalizeIdentity(identity)
	if err != nil {
		return EnrollmentToken{}, err
	}
	now := s.now().UTC()
	expiresAt = expiresAt.UTC()
	if !expiresAt.After(now) {
		return EnrollmentToken{}, errors.New("federation enrollment token expiry must be in the future")
	}
	token, err := randomEnrollmentToken()
	if err != nil {
		return EnrollmentToken{}, err
	}
	record := oneTimeTokenRecord{
		Digest: digestEnrollmentToken(token), ExpiresAt: expiresAt,
		HubID: identity.NodeID, HubName: identity.Name,
		HubURL: identity.BaseURL,
	}
	if err := s.mutate(func(state *persistedEnrollmentStore) error {
		state.Tokens = append(state.Tokens, record)
		return nil
	}); err != nil {
		return EnrollmentToken{}, err
	}
	return EnrollmentToken{
		Token: token, ExpiresAt: expiresAt,
		HubID: identity.NodeID, HubName: identity.Name,
		HubURL: identity.BaseURL, ProtocolVersion: ProtocolVersion,
	}, nil
}

func (s *Store) Begin(
	ctx context.Context, token string, request JoinRequest,
) (Enrollment, error) {
	if err := ctx.Err(); err != nil {
		return Enrollment{}, err
	}
	request, err := normalizeJoinRequest(request)
	if err != nil {
		return Enrollment{}, err
	}
	now := s.now().UTC()
	var result Enrollment
	err = s.mutate(func(state *persistedEnrollmentStore) error {
		tokenIndex := findToken(state.Tokens, token)
		if tokenIndex < 0 {
			return ErrEnrollmentTokenInvalid
		}
		tokenRecord := &state.Tokens[tokenIndex]
		if !tokenRecord.ExpiresAt.After(now) {
			return ErrEnrollmentTokenExpired
		}
		if tokenRecord.ConsumedEnrollmentID != "" {
			return ErrEnrollmentTokenConsumed
		}
		if request.NodeID == tokenRecord.HubID {
			return ErrEnrollmentConflict
		}

		existingIndex := enrollmentIndexByID(state.Enrollments, request.EnrollmentID)
		if existingIndex >= 0 {
			existing := &state.Enrollments[existingIndex]
			if existing.NodeID != request.NodeID {
				return ErrEnrollmentConflict
			}
			if existing.SpokeBaseURL != request.BaseURL {
				return ErrDuplicateNodeID
			}
			if existing.HubID != tokenRecord.HubID ||
				existing.HubURL != tokenRecord.HubURL {
				return ErrEnrollmentConflict
			}
			if existing.State == EnrollmentRevoked {
				return ErrEnrollmentRevoked
			}
			if existing.State != EnrollmentPending {
				return ErrEnrollmentTokenConsumed
			}
			existing.ExpiresAt = tokenRecord.ExpiresAt
			existing.UpdatedAt = now
			tokenRecord.ConsumedEnrollmentID = existing.ID
			tokenRecord.ConsumedNodeID = existing.NodeID
			result = *existing
			return nil
		}

		for _, existing := range state.Enrollments {
			if existing.State == EnrollmentRevoked {
				continue
			}
			if existing.NodeID == request.NodeID {
				return ErrDuplicateNodeID
			}
			if existing.SpokeBaseURL == request.BaseURL {
				return ErrDuplicateOrigin
			}
		}
		result = Enrollment{
			ID:     request.EnrollmentID,
			NodeID: request.NodeID, SpokeName: request.Name,
			SpokePlatform: request.Platform, SpokeBaseURL: request.BaseURL,
			HubID:           tokenRecord.HubID,
			HubName:         tokenRecord.HubName,
			HubURL:          tokenRecord.HubURL,
			ProtocolVersion: ProtocolVersion, State: EnrollmentPending,
			ExpiresAt: tokenRecord.ExpiresAt, CreatedAt: now, UpdatedAt: now,
		}
		state.Enrollments = append(state.Enrollments, result)
		tokenRecord.ConsumedEnrollmentID = result.ID
		tokenRecord.ConsumedNodeID = result.NodeID
		return nil
	})
	return result, err
}

func (s *Store) Resume(
	ctx context.Context, enrollmentID, nodeID string,
) (Enrollment, error) {
	if err := ctx.Err(); err != nil {
		return Enrollment{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	index := enrollmentIndexByID(s.state.Enrollments, enrollmentID)
	if index < 0 || s.state.Enrollments[index].NodeID != nodeID {
		return Enrollment{}, ErrEnrollmentNotFound
	}
	return s.state.Enrollments[index], nil
}

// Get returns an enrollment by its hub-assigned ID.
func (s *Store) Get(ctx context.Context, enrollmentID string) (Enrollment, error) {
	if err := ctx.Err(); err != nil {
		return Enrollment{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	index := enrollmentIndexByID(s.state.Enrollments, enrollmentID)
	if index < 0 {
		return Enrollment{}, ErrEnrollmentNotFound
	}
	return s.state.Enrollments[index], nil
}

func (s *Store) MarkPreparationStarted(
	ctx context.Context, enrollmentID string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.mutate(func(state *persistedEnrollmentStore) error {
		index := enrollmentIndexByID(state.Enrollments, enrollmentID)
		if index < 0 {
			return ErrEnrollmentNotFound
		}
		enrollment := &state.Enrollments[index]
		if enrollment.State == EnrollmentRevoked {
			return ErrEnrollmentRevoked
		}
		enrollment.PreparationStarted = true
		enrollment.UpdatedAt = s.now().UTC()
		return nil
	})
}

// MarkLocalPreparationStarted records that the hub pinned the current
// pending enrollment. A pinned enrollment is no longer governed by the
// one-time token deadline and remains recoverable until activation or abort.
func (s *Store) MarkLocalPreparationStarted(
	ctx context.Context, enrollmentID string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	enrollmentID = strings.TrimSpace(enrollmentID)
	return s.mutate(func(state *persistedEnrollmentStore) error {
		if state.Local == nil || state.Local.EnrollmentID != enrollmentID ||
			state.Local.State != EnrollmentPending {
			return ErrEnrollmentConflict
		}
		state.Local.PreparationStarted = true
		return nil
	})
}

func (s *Store) Activate(ctx context.Context, enrollmentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.mutate(func(state *persistedEnrollmentStore) error {
		index := enrollmentIndexByID(state.Enrollments, enrollmentID)
		if index < 0 {
			return ErrEnrollmentNotFound
		}
		enrollment := &state.Enrollments[index]
		if enrollment.State == EnrollmentRevoked {
			return ErrEnrollmentRevoked
		}
		enrollment.State = EnrollmentActive
		enrollment.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Store) Revoke(ctx context.Context, enrollmentID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.mutate(func(state *persistedEnrollmentStore) error {
		index := enrollmentIndexByID(state.Enrollments, enrollmentID)
		if index < 0 {
			return ErrEnrollmentNotFound
		}
		enrollment := &state.Enrollments[index]
		if enrollment.State == EnrollmentRevoked {
			return nil
		}
		now := s.now().UTC()
		enrollment.State = EnrollmentRevoked
		enrollment.RevokedAt = now
		enrollment.UpdatedAt = now
		return nil
	})
}

func (s *Store) CleanupExpired(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	now := s.now().UTC()
	removed := 0
	err := s.mutate(func(state *persistedEnrollmentStore) error {
		state.Enrollments = slices.DeleteFunc(state.Enrollments, func(enrollment Enrollment) bool {
			remove := enrollment.State == EnrollmentPending &&
				!enrollment.PreparationStarted && !enrollment.ExpiresAt.After(now)
			if remove {
				removed++
			}
			return remove
		})
		state.Tokens = slices.DeleteFunc(state.Tokens, func(token oneTimeTokenRecord) bool {
			return !token.ExpiresAt.After(now)
		})
		return nil
	})
	return removed, err
}

func (s *Store) SaveLocal(ctx context.Context, local LocalEnrollment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := normalizeLocalEnrollment(local)
	if err != nil {
		return err
	}
	return s.mutate(func(state *persistedEnrollmentStore) error {
		state.Local = &normalized
		return nil
	})
}

// SaveLocalPreparationSeal binds a hub-issued preparation seal to the
// current pending local enrollment. A seal for any other enrollment, spoke,
// hub, or protocol is rejected without changing durable state.
func (s *Store) SaveLocalPreparationSeal(
	ctx context.Context,
	seal LocalPreparationSeal,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	seal = normalizeLocalPreparationSeal(seal)
	if err := validateLocalPreparationSeal(seal); err != nil {
		return err
	}
	return s.mutate(func(state *persistedEnrollmentStore) error {
		if state.Local == nil || !state.Local.PreparationStarted ||
			!localPreparationSealMatches(*state.Local, seal) {
			return ErrPreparationSealMismatch
		}
		if state.Local.State != EnrollmentPending {
			return ErrPreparationSealMismatch
		}
		if existing := state.Local.Preparation; existing != nil && *existing != seal {
			return ErrPreparationSealMismatch
		}
		stored := seal
		state.Local.Preparation = &stored
		return nil
	})
}

// MarkLocalActive records the hub's idempotent activation response and the
// bounded lease during which the spoke may accept requests from that hub. It
// refuses to activate a different local enrollment or persist an expired
// lease.
func (s *Store) MarkLocalActive(
	ctx context.Context, enrollmentID string, activationValidUntil time.Time,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	enrollmentID = strings.TrimSpace(enrollmentID)
	activationValidUntil = activationValidUntil.UTC()
	if !activationValidUntil.After(s.now().UTC()) {
		return errors.New("federation activation lease must expire in the future")
	}
	return s.mutate(func(state *persistedEnrollmentStore) error {
		if state.Local == nil || state.Local.EnrollmentID != enrollmentID ||
			state.Local.Preparation == nil {
			return ErrPreparationSealMismatch
		}
		state.Local.State = EnrollmentActive
		state.Local.PreparationRequired = false
		state.Local.ActivationValidUntil = activationValidUntil
		return nil
	})
}

// InvalidateLocalActivationLease stops an active spoke from accepting hub
// requests after the hub definitively rejects the enrollment.
func (s *Store) InvalidateLocalActivationLease(
	ctx context.Context, enrollmentID string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	enrollmentID = strings.TrimSpace(enrollmentID)
	return s.mutate(func(state *persistedEnrollmentStore) error {
		if state.Local == nil || state.Local.EnrollmentID != enrollmentID {
			return ErrEnrollmentConflict
		}
		state.Local.ActivationValidUntil = time.Time{}
		return nil
	})
}

func (s *Store) Local() (LocalEnrollment, bool) {
	if s == nil {
		return LocalEnrollment{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.Local == nil {
		return LocalEnrollment{}, false
	}
	return cloneLocalEnrollment(*s.state.Local), true
}

func (s *Store) ClearLocal(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.mutate(func(state *persistedEnrollmentStore) error {
		state.Local = nil
		return nil
	})
}

func (s *Store) List() []Enrollment {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.state.Enrollments)
}

// EnrollmentForSpoke returns the one non-revoked hub-side enrollment
// for a peer identity. Store validation rejects multiple live enrollments for
// one spoke, while revoked records remain only as audit history.
func (s *Store) EnrollmentForSpoke(nodeID string) (Enrollment, bool) {
	if s == nil {
		return Enrollment{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, enrollment := range s.state.Enrollments {
		if enrollment.NodeID == nodeID && enrollment.State != EnrollmentRevoked {
			return enrollment, true
		}
	}
	return Enrollment{}, false
}

func (s *Store) mutate(apply func(*persistedEnrollmentStore) error) error {
	if s == nil {
		return errors.New("federation enrollment store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneEnrollmentStore(s.state)
	if err := apply(&next); err != nil {
		return err
	}
	sortEnrollmentStore(&next)
	if err := writeEnrollmentStore(s.path, next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func normalizeJoinRequest(request JoinRequest) (JoinRequest, error) {
	request.EnrollmentID = strings.TrimSpace(request.EnrollmentID)
	request.NodeID = strings.TrimSpace(request.NodeID)
	request.Name = strings.TrimSpace(request.Name)
	request.Platform = strings.TrimSpace(request.Platform)
	if !validID(request.EnrollmentID) {
		return JoinRequest{}, errors.New("federation enrollment ID must be 32 lowercase hexadecimal characters")
	}
	if !validID(request.NodeID) {
		return JoinRequest{}, errors.New("federation node ID must be 32 lowercase hexadecimal characters")
	}
	if request.ProtocolVersion != ProtocolVersion {
		return JoinRequest{}, fmt.Errorf(
			"%w: expected %d, got %d", ErrProtocolMismatch,
			ProtocolVersion, request.ProtocolVersion,
		)
	}
	if request.Platform == "" {
		return JoinRequest{}, errors.New("federation spoke platform is required")
	}
	baseURL, err := CanonicalOrigin(request.BaseURL)
	if err != nil {
		return JoinRequest{}, err
	}
	request.BaseURL = baseURL
	if request.HubCredential == "" ||
		len(request.HubCredential) > MaxCredentialLength ||
		strings.TrimSpace(request.HubCredential) != request.HubCredential ||
		strings.ContainsAny(request.HubCredential, " \t\r\n") {
		return JoinRequest{}, errors.New("hub-to-spoke credential is invalid")
	}
	return request, nil
}

func normalizeLocalEnrollment(local LocalEnrollment) (LocalEnrollment, error) {
	return normalizeLocalEnrollmentVersioned(local, true)
}

func normalizePersistedLocalEnrollment(local LocalEnrollment) (LocalEnrollment, error) {
	return normalizeLocalEnrollmentVersioned(local, false)
}

func normalizeLocalEnrollmentVersioned(
	local LocalEnrollment,
	requireCurrentProtocol bool,
) (LocalEnrollment, error) {
	local.EnrollmentID = strings.TrimSpace(local.EnrollmentID)
	local.NodeID = strings.TrimSpace(local.NodeID)
	local.SpokeName = strings.TrimSpace(local.SpokeName)
	local.SpokePlatform = strings.TrimSpace(local.SpokePlatform)
	local.HubID = strings.TrimSpace(local.HubID)
	local.HubName = strings.TrimSpace(local.HubName)
	if !validID(local.EnrollmentID) || !validID(local.NodeID) {
		return LocalEnrollment{}, errors.New("local federation enrollment has invalid identity")
	}
	if local.HubID != "" && !validID(local.HubID) {
		return LocalEnrollment{}, errors.New("local federation enrollment has invalid hub identity")
	}
	if requireCurrentProtocol && local.ProtocolVersion != ProtocolVersion {
		return LocalEnrollment{}, ErrProtocolMismatch
	}
	if local.ProtocolVersion <= 0 {
		return LocalEnrollment{}, errors.New("local federation enrollment has invalid protocol version")
	}
	if local.State != EnrollmentPending && local.State != EnrollmentActive &&
		local.State != EnrollmentRevoked {
		return LocalEnrollment{}, errors.New("local federation enrollment has invalid state")
	}
	if local.ExpiresAt.IsZero() && local.HubID != "" {
		return LocalEnrollment{}, errors.New("local federation enrollment has no expiry")
	}
	if !local.ExpiresAt.IsZero() {
		local.ExpiresAt = local.ExpiresAt.UTC()
	}
	if !local.ActivationValidUntil.IsZero() {
		local.ActivationValidUntil = local.ActivationValidUntil.UTC()
	}
	var err error
	local.SpokeBaseURL, err = CanonicalOrigin(local.SpokeBaseURL)
	if err != nil {
		return LocalEnrollment{}, fmt.Errorf("local spoke origin: %w", err)
	}
	local.HubURL, err = CanonicalOrigin(local.HubURL)
	if err != nil {
		return LocalEnrollment{}, fmt.Errorf("local hub origin: %w", err)
	}
	if local.Preparation != nil {
		if !local.PreparationStarted {
			return LocalEnrollment{}, ErrPreparationSealMismatch
		}
		normalized := normalizeLocalPreparationSeal(*local.Preparation)
		if err := validateLocalPreparationSealVersioned(
			normalized, requireCurrentProtocol,
		); err != nil {
			return LocalEnrollment{}, err
		}
		if !localPreparationSealMatches(local, normalized) {
			return LocalEnrollment{}, ErrPreparationSealMismatch
		}
		local.Preparation = &normalized
	}
	return local, nil
}

func normalizeLocalPreparationSeal(seal LocalPreparationSeal) LocalPreparationSeal {
	seal.EnrollmentID = strings.TrimSpace(seal.EnrollmentID)
	seal.NodeID = strings.TrimSpace(seal.NodeID)
	seal.HubID = strings.TrimSpace(seal.HubID)
	seal.PreparationDigest = strings.TrimSpace(seal.PreparationDigest)
	seal.Seal = strings.TrimSpace(seal.Seal)
	return seal
}

func validateLocalPreparationSeal(seal LocalPreparationSeal) error {
	return validateLocalPreparationSealVersioned(seal, true)
}

func validateLocalPreparationSealVersioned(
	seal LocalPreparationSeal,
	requireCurrentProtocol bool,
) error {
	if !validID(seal.EnrollmentID) || !validID(seal.NodeID) ||
		!validID(seal.HubID) {
		return errors.New("local federation preparation seal has invalid identity")
	}
	if requireCurrentProtocol && seal.ProtocolVersion != ProtocolVersion {
		return ErrProtocolMismatch
	}
	if seal.ProtocolVersion <= 0 {
		return errors.New("local federation preparation seal has invalid protocol version")
	}
	if seal.PreparationDigest == "" || seal.Seal == "" ||
		strings.ContainsAny(seal.PreparationDigest, " \t\r\n") ||
		strings.ContainsAny(seal.Seal, " \t\r\n") {
		return errors.New("local federation preparation seal is incomplete")
	}
	return nil
}

func localPreparationSealMatches(
	local LocalEnrollment,
	seal LocalPreparationSeal,
) bool {
	return local.EnrollmentID == seal.EnrollmentID &&
		local.NodeID == seal.NodeID &&
		local.HubID == seal.HubID &&
		local.ProtocolVersion == seal.ProtocolVersion
}

func randomEnrollmentToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate federation enrollment token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func digestEnrollmentToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func findToken(tokens []oneTimeTokenRecord, raw string) int {
	digest := digestEnrollmentToken(raw)
	match := -1
	for index, token := range tokens {
		if subtle.ConstantTimeCompare([]byte(digest), []byte(token.Digest)) == 1 {
			match = index
		}
	}
	return match
}

func enrollmentIndexByID(enrollments []Enrollment, enrollmentID string) int {
	for index, enrollment := range enrollments {
		if enrollment.ID == enrollmentID {
			return index
		}
	}
	return -1
}

func readEnrollmentStore(path string) (persistedEnrollmentStore, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return persistedEnrollmentStore{}, false, nil
	}
	if err != nil {
		return persistedEnrollmentStore{}, false, fmt.Errorf("open federation enrollment store: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var state persistedEnrollmentStore
	if err := decoder.Decode(&state); err != nil {
		return persistedEnrollmentStore{}, false, fmt.Errorf("decode federation enrollment store: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return persistedEnrollmentStore{}, false, errors.New("decode federation enrollment store: multiple JSON values")
		}
		return persistedEnrollmentStore{}, false, fmt.Errorf("decode federation enrollment store trailer: %w", err)
	}
	if err := validateEnrollmentStore(&state); err != nil {
		return persistedEnrollmentStore{}, false, err
	}
	return state, true, nil
}

func validateEnrollmentStore(state *persistedEnrollmentStore) error {
	if state.Version != enrollmentStoreVersion {
		return fmt.Errorf("federation enrollment store version must be %d, got %d", enrollmentStoreVersion, state.Version)
	}
	for index, token := range state.Tokens {
		digest, err := hex.DecodeString(token.Digest)
		if err != nil || len(digest) != sha256.Size || token.Digest != strings.ToLower(token.Digest) {
			return fmt.Errorf("federation enrollment token %d has invalid digest", index)
		}
		if !validID(token.HubID) {
			return fmt.Errorf("federation enrollment token %d has invalid hub ID", index)
		}
		if token.ExpiresAt.IsZero() {
			return fmt.Errorf("federation enrollment token %d has no expiry", index)
		}
		if (token.ConsumedEnrollmentID == "") != (token.ConsumedNodeID == "") ||
			(token.ConsumedEnrollmentID != "" &&
				(!validID(token.ConsumedEnrollmentID) || !validID(token.ConsumedNodeID))) {
			return fmt.Errorf("federation enrollment token %d has invalid consumption binding", index)
		}
		canonical, err := CanonicalOrigin(token.HubURL)
		if err != nil || canonical != token.HubURL {
			return fmt.Errorf("federation enrollment token %d has non-canonical hub origin", index)
		}
	}
	ids := make(map[string]struct{}, len(state.Enrollments))
	nodeIDs := make(map[string]struct{}, len(state.Enrollments))
	origins := make(map[string]struct{}, len(state.Enrollments))
	for index, enrollment := range state.Enrollments {
		if !validID(enrollment.ID) || !validID(enrollment.NodeID) || !validID(enrollment.HubID) {
			return fmt.Errorf("federation enrollment %d has invalid identity", index)
		}
		if enrollment.ProtocolVersion != ProtocolVersion {
			return fmt.Errorf("federation enrollment %d: %w", index, ErrProtocolMismatch)
		}
		if enrollment.State != EnrollmentPending && enrollment.State != EnrollmentActive &&
			enrollment.State != EnrollmentRevoked {
			return fmt.Errorf("federation enrollment %d has invalid state %q", index, enrollment.State)
		}
		spokeOrigin, err := CanonicalOrigin(enrollment.SpokeBaseURL)
		if err != nil || spokeOrigin != enrollment.SpokeBaseURL {
			return fmt.Errorf("federation enrollment %d has non-canonical spoke origin", index)
		}
		hubOrigin, err := CanonicalOrigin(enrollment.HubURL)
		if err != nil || hubOrigin != enrollment.HubURL {
			return fmt.Errorf("federation enrollment %d has non-canonical hub origin", index)
		}
		if _, duplicate := ids[enrollment.ID]; duplicate {
			return fmt.Errorf("duplicate federation enrollment ID %s", enrollment.ID)
		}
		ids[enrollment.ID] = struct{}{}
		if enrollment.State != EnrollmentRevoked {
			if _, duplicate := nodeIDs[enrollment.NodeID]; duplicate {
				return fmt.Errorf("duplicate active federation node ID %s", enrollment.NodeID)
			}
			if _, duplicate := origins[enrollment.SpokeBaseURL]; duplicate {
				return fmt.Errorf("duplicate active federation origin %s", enrollment.SpokeBaseURL)
			}
			nodeIDs[enrollment.NodeID] = struct{}{}
			origins[enrollment.SpokeBaseURL] = struct{}{}
		}
	}
	if state.Local != nil {
		normalized, err := normalizePersistedLocalEnrollment(*state.Local)
		if err != nil {
			return err
		}
		state.Local = &normalized
	}
	sortEnrollmentStore(state)
	return nil
}

func writeEnrollmentStore(path string, state persistedEnrollmentStore) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".federation-enrollments.*.tmp")
	if err != nil {
		return fmt.Errorf("create federation enrollment store temp file: %w", err)
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
		return fmt.Errorf("restrict federation enrollment store temp file: %w", err)
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode federation enrollment store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync federation enrollment store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close federation enrollment store: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish federation enrollment store: %w", err)
	}
	committed = true
	return nil
}

func cloneEnrollmentStore(state persistedEnrollmentStore) persistedEnrollmentStore {
	clone := persistedEnrollmentStore{
		Version: state.Version,
		Tokens:  slices.Clone(state.Tokens), Enrollments: slices.Clone(state.Enrollments),
	}
	if state.Local != nil {
		local := cloneLocalEnrollment(*state.Local)
		clone.Local = &local
	}
	return clone
}

func cloneLocalEnrollment(local LocalEnrollment) LocalEnrollment {
	if local.Preparation != nil {
		preparation := *local.Preparation
		local.Preparation = &preparation
	}
	return local
}

func sortEnrollmentStore(state *persistedEnrollmentStore) {
	slices.SortFunc(state.Tokens, func(left, right oneTimeTokenRecord) int {
		return strings.Compare(left.Digest, right.Digest)
	})
	slices.SortFunc(state.Enrollments, func(left, right Enrollment) int {
		return strings.Compare(left.ID, right.ID)
	})
}
