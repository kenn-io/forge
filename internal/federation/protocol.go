// Package federation defines the exact-version wire and durable enrollment
// contracts shared by hubs and spokes.
package federation

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ProtocolVersion is the first full Forge federation contract. Peers require
// an exact match; there is no version translation.
const ProtocolVersion = 3

// SpokeActivationLeaseDuration bounds how long a spoke may accept hub-issued
// federation requests without successfully revalidating its enrollment.
const SpokeActivationLeaseDuration = 24 * time.Hour

// MaxCredentialLength bounds opaque federation bearers on the wire and disk.
const MaxCredentialLength = 256

type EnrollmentState string

const (
	EnrollmentPending EnrollmentState = "pending"
	EnrollmentActive  EnrollmentState = "active"
	EnrollmentRevoked EnrollmentState = "revoked"
)

var (
	ErrEnrollmentNotFound      = errors.New("federation enrollment not found")
	ErrEnrollmentTokenInvalid  = errors.New("federation enrollment token is invalid")
	ErrEnrollmentTokenExpired  = errors.New("federation enrollment token has expired")
	ErrEnrollmentTokenConsumed = errors.New(
		"federation enrollment token has already been consumed",
	)
	ErrEnrollmentConflict  = errors.New("federation enrollment conflicts with existing state")
	ErrDuplicateNodeID     = errors.New("federation node ID is already enrolled at another origin")
	ErrDuplicateOrigin     = errors.New("federation origin is already enrolled to another spoke")
	ErrProtocolMismatch    = errors.New("federation protocol version mismatch")
	ErrEnrollmentRevoked   = errors.New("federation enrollment is revoked")
	ErrPreparationRequired = errors.New(
		"federation spoke preparation is required",
	)
	ErrPreparationSealMismatch = errors.New(
		"federation spoke preparation seal does not match the local enrollment",
	)
)

// Identity is the stable identity and public HTTPS origin of one daemon.
type Identity struct {
	NodeID  string `json:"node_id"`
	Name    string `json:"name,omitempty"`
	BaseURL string `json:"base_url"`
}

// EnrollmentToken is returned exactly once to the local hub user.
type EnrollmentToken struct {
	Token           string    `json:"token"`
	ExpiresAt       time.Time `json:"expires_at"`
	HubID           string    `json:"hub_node_id"`
	HubName         string    `json:"hub_name,omitempty"`
	HubURL          string    `json:"hub_base_url"`
	ProtocolVersion int       `json:"protocol_version"`
}

// JoinRequest is sent by a joining spoke to the hub. EnrollmentID is
// generated and persisted by the spoke before the request, making retries safe.
type JoinRequest struct {
	EnrollmentID    string `json:"enrollment_id"`
	NodeID          string `json:"node_id"`
	Name            string `json:"name,omitempty"`
	Platform        string `json:"platform"`
	BaseURL         string `json:"base_url"`
	ProtocolVersion int    `json:"protocol_version"`
	HubCredential   string `json:"hub_credential" maxLength:"256"`
}

// JoinResponse gives the spoke its hub binding and outbound bearer.
// The hub may rotate SpokeCredential on an idempotent retry.
type JoinResponse struct {
	EnrollmentID        string          `json:"enrollment_id"`
	HubID               string          `json:"hub_node_id"`
	HubName             string          `json:"hub_name,omitempty"`
	HubURL              string          `json:"hub_base_url"`
	SpokeCredential     string          `json:"spoke_credential"`
	ProtocolVersion     int             `json:"protocol_version"`
	State               EnrollmentState `json:"state"`
	ExpiresAt           time.Time       `json:"expires_at"`
	PreparationRequired bool            `json:"preparation_required"`
}

// Enrollment is the hub's durable membership record. Bearers are
// deliberately absent and live only in federationauth.
type Enrollment struct {
	ID                   string          `json:"id"`
	NodeID               string          `json:"node_id"`
	SpokeName            string          `json:"spoke_name,omitempty"`
	SpokePlatform        string          `json:"spoke_platform"`
	SpokeBaseURL         string          `json:"spoke_base_url"`
	HubID                string          `json:"hub_node_id"`
	HubName              string          `json:"hub_name,omitempty"`
	HubURL               string          `json:"hub_base_url"`
	ProtocolVersion      int             `json:"protocol_version"`
	State                EnrollmentState `json:"state"`
	ExpiresAt            time.Time       `json:"expires_at"`
	PreparationStarted   bool            `json:"preparation_started,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	RevokedAt            time.Time       `json:"revoked_at,omitzero"`
	ActivationValidUntil time.Time       `json:"activation_valid_until,omitzero"`
}

// LocalEnrollment is the joining daemon's durable view of its pending or
// active hub binding. It never contains either bearer credential.
type LocalEnrollment struct {
	EnrollmentID         string                `json:"enrollment_id"`
	NodeID               string                `json:"node_id"`
	SpokeName            string                `json:"spoke_name,omitempty"`
	SpokePlatform        string                `json:"spoke_platform"`
	SpokeBaseURL         string                `json:"spoke_base_url"`
	HubID                string                `json:"hub_node_id,omitempty"`
	HubName              string                `json:"hub_name,omitempty"`
	HubURL               string                `json:"hub_base_url"`
	ProtocolVersion      int                   `json:"protocol_version"`
	State                EnrollmentState       `json:"state"`
	ExpiresAt            time.Time             `json:"expires_at"`
	PreparationStarted   bool                  `json:"preparation_started,omitempty"`
	PreparationRequired  bool                  `json:"preparation_required"`
	Preparation          *LocalPreparationSeal `json:"preparation,omitempty"`
	ActivationValidUntil time.Time             `json:"activation_valid_until,omitzero"`
}

// LocalPreparationSeal is the joining daemon's durable proof that the
// hub sealed the exact enrollment and preparation digest that will be
// activated after restart. The opaque seal never appears in CLI output.
type LocalPreparationSeal struct {
	EnrollmentID      string `json:"enrollment_id"`
	NodeID            string `json:"node_id"`
	HubID             string `json:"hub_node_id"`
	ProtocolVersion   int    `json:"protocol_version"`
	PreparationDigest string `json:"preparation_digest"`
	Seal              string `json:"preparation_seal"`
}

// CanonicalOrigin validates and canonicalizes one production federation
// origin. Only HTTPS origins are accepted.
func CanonicalOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse federation origin: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.Opaque != "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return "", fmt.Errorf("federation origin must be an absolute HTTPS origin, got %q", raw)
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname == "" || strings.Contains(hostname, "%") {
		return "", fmt.Errorf("federation origin has invalid host %q", u.Host)
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("federation origin has an empty port")
	}
	if port != "" {
		parsedPort, parseErr := strconv.Atoi(port)
		if parseErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", fmt.Errorf("federation origin has invalid port %q", port)
		}
		port = strconv.Itoa(parsedPort)
	}
	if port == "443" {
		port = ""
	}
	host := hostname
	if ip := net.ParseIP(hostname); ip != nil && strings.Contains(hostname, ":") {
		host = "[" + strings.ToLower(ip.String()) + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	return "https://" + host, nil
}

// ValidNodeID reports whether value is an exact stable Forge daemon identity.
func ValidNodeID(value string) bool {
	return validID(value)
}

// NewID returns a random 128-bit lowercase hexadecimal protocol identifier.
func NewID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate federation ID: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func validID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func normalizeIdentity(identity Identity) (Identity, error) {
	identity.NodeID = strings.TrimSpace(identity.NodeID)
	identity.Name = strings.TrimSpace(identity.Name)
	if !validID(identity.NodeID) {
		return Identity{}, errors.New("federation identity node ID must be 32 lowercase hexadecimal characters")
	}
	baseURL, err := CanonicalOrigin(identity.BaseURL)
	if err != nil {
		return Identity{}, err
	}
	identity.BaseURL = baseURL
	return identity, nil
}
